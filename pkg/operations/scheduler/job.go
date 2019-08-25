/*
Copyright 2019 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"strings"

	"go.uber.org/zap"
	"knative.dev/pkg/kmeta"
	"knative.dev/pkg/logging"

	//	schedulerv1 "cloud.google.com/go/scheduler/apiv1"
	"github.com/google/knative-gcp/pkg/operations"
	//	"google.golang.org/grpc/codes"
	//	gstatus "google.golang.org/grpc/status"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	// Mapping of the storage importer eventTypes to google storage types.
	storageEventTypes = map[string]string{
		"finalize":       "OBJECT_FINALIZE",
		"archive":        "OBJECT_ARCHIVE",
		"delete":         "OBJECT_DELETE",
		"metadataUpdate": "OBJECT_METADATA_UPDATE",
	}
)

// TODO: the job could output the resolved projectID.
type JobActionResult struct {
	// Result is the result the operation attempted.
	Result bool `json:"result,omitempty"`
	// Error is the error string if failure occurred
	Error string `json:"error,omitempty"`
	// JobId holds the notification ID for GCS
	// and is filled in during create operation.
	JobId string `json:"notificationId,omitempty"`
	// Project is the project id that we used (this might have
	// been defaulted, to we'll expose it).
	ProjectId string `json:"projectId,omitempty"`
}

// JobArgs are the configuration required to make a NewJobOps.
type JobArgs struct {
	// UID of the resource that caused the action to be taken. Will
	// be added as a label to the podtemplate.
	UID string
	// Image is the actual binary that we'll run to operate on the
	// notification.
	Image string
	// Action is what the binary should do
	Action    string
	ProjectID string
	// Bucket
	Bucket string
	// TopicID we'll use for pubsub target.
	TopicID string
	// JobId is the notifification ID that GCS gives
	// back to us. We need that to delete it.
	JobId string
	// EventTypes is an array of strings specifying which
	// event types we want the notification to fire on.
	EventTypes []string
	// ObjectNamePrefix is an optional filter
	ObjectNamePrefix string
	// CustomAttributes is the list of additional attributes to have
	// GCS supply back to us when it sends a notification.
	CustomAttributes map[string]string
	Secret           corev1.SecretKeySelector
	Owner            kmeta.OwnerRefable
}

// NewJobOps returns a new batch Job resource.
func NewJobOps(arg JobArgs) *batchv1.Job {
	env := []corev1.EnvVar{{
		Name:  "ACTION",
		Value: arg.Action,
	}, {
		Name:  "PROJECT_ID",
		Value: arg.ProjectID,
	}, {
		Name:  "BUCKET",
		Value: arg.Bucket,
	}}

	switch arg.Action {
	case operations.ActionCreate:
		env = append(env, []corev1.EnvVar{
			{
				Name:  "EVENT_TYPES",
				Value: strings.Join(arg.EventTypes, ":"),
			}, {
				Name:  "PUBSUB_TOPIC_ID",
				Value: arg.TopicID,
			}}...)
	case operations.ActionDelete:
		env = append(env, []corev1.EnvVar{{
			Name:  "NOTIFICATION_ID",
			Value: arg.JobId,
		}}...)
	}

	podTemplate := operations.MakePodTemplate(arg.Image, arg.UID, arg.Action, arg.Secret, env...)

	backoffLimit := int32(3)
	parallelism := int32(1)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            SchedulerJobName(arg.Owner, arg.Action),
			Namespace:       arg.Owner.GetObjectMeta().GetNamespace(),
			Labels:          SchedulerJobLabels(arg.Owner, arg.Action),
			OwnerReferences: []metav1.OwnerReference{*kmeta.NewControllerRef(arg.Owner)},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Parallelism:  &parallelism,
			Template:     *podTemplate,
		},
	}
}

// JobOps defines the configuration to use for this operation.
type JobOps struct {
	SchedulerOps

	// Action is the operation the job should run.
	// Options: [exists, create, delete]
	Action string `envconfig:"ACTION" required:"true"`

	// Topic is the environment variable containing the PubSub Topic being
	// subscribed to's name. In the form that is unique within the project.
	// E.g. 'laconia', not 'projects/my-gcp-project/topics/laconia'.
	Topic string `envconfig:"PUBSUB_TOPIC_ID" required:"true"`

	// Bucket to operate on
	Bucket string `envconfig:"BUCKET" required:"true"`

	// JobId is the environment variable containing the name of the
	// subscription to use.
	JobId string `envconfig:"NOTIFICATION_ID" required:"false" default:""`

	// EventTypes is a : separated eventtypes, if omitted all will be used.
	// TODO: Look at native envconfig list support
	EventTypes string `envconfig:"EVENT_TYPES" required:"false" default:""`

	// ObjectNamePrefix is an optional filter for the GCS
	ObjectNamePrefix string `envconfig:"OBJECT_NAME_PREFIX" required:"false" default:""`

	// TODO; Add support for custom attributes. Look at using envconfig Map with
	// necessary encoding / decoding.
}

// Run will perform the action configured upon a subscription.
func (n *JobOps) Run(ctx context.Context) error {
	if n.client == nil {
		return errors.New("pub/sub client is nil")
	}
	logger := logging.FromContext(ctx)

	logger = logger.With(
		zap.String("action", n.Action),
		zap.String("project", n.Project),
		zap.String("topic", n.Topic),
		zap.String("subscription", n.JobId),
	)

	logger.Info("Storage Job Job.")

	// Load the Bucket.
	//	bucket := n.Client.Bucket(n.Bucket)

	switch n.Action {
	case operations.ActionExists:
		// If notification doesn't exist, that is an error.
		logger.Info("Previously created.")

	case operations.ActionCreate:
		// logger.Info("CREATING")
		/*
			customAttributes := make(map[string]string)

			// Add our own event type here...
			customAttributes["knative-gcp"] = "google.storage"

			eventTypes := strings.Split(n.EventTypes, ":")
			logger.Infof("Creating a notification on bucket %s", n.Bucket)

			nc := n.client.job{
				TopicProjectID:   n.Project,
				TopicID:          n.Topic,
				PayloadFormat:    storageClient.JSONPayload,
				EventTypes:       n.toStorageEventTypes(eventTypes),
				ObjectNamePrefix: n.ObjectNamePrefix,
				CustomAttributes: customAttributes,
			}

			notification, err := bucket.AddJob(ctx, &nc)
			if err != nil {
				result := &JobActionResult{
					Result: false,
					Error:  err.Error(),
				}
				logger.Infof("Failed to create Job: %s", err)
				err = n.writeTerminationMessage(result)
				return err
			}
			logger.Infof("Created Job %q", notification.ID)
			result := &JobActionResult{
				Result: true,
				JobId:  notification.ID,
			}
			err = n.writeTerminationMessage(result)
			if err != nil {
				logger.Infof("Failed to write termination message: %s", err)
				return err
			}
		*/
	case operations.ActionDelete:
		logger.Infof("DELETE")
		/*
			notifications, err := bucket.Jobs(ctx)
			if err != nil {
				logger.Infof("Failed to fetch existing notifications: %s", err)
				return err
			}

			// This is bit wonky because, we could always just try to delete, but figuring out
			// if an error returned is NotFound seems to not really work, so, we'll try
			// checking first the list and only then deleting.
			notificationId := n.JobId
			if notificationId != "" {
				if existing, ok := notifications[notificationId]; ok {
					logger.Infof("Found existing notification: %+v", existing)
					logger.Infof("Deleting notification as: %q", notificationId)
					err = bucket.DeleteJob(ctx, notificationId)
					if err == nil {
						logger.Infof("Deleted Job: %q", notificationId)
						err = n.writeTerminationMessage(&JobActionResult{Result: true})
						if err != nil {
							logger.Infof("Failed to write termination message: %s", err)
							return err
						}
						return nil
					}

					if st, ok := gstatus.FromError(err); !ok {
						logger.Infof("error from the cloud storage client: %s", err)
						writeErr := n.writeTerminationMessage(&JobActionResult{Result: false, Error: err.Error()})
						if writeErr != nil {
							logger.Infof("Failed to write termination message: %s", writeErr)
							return err
						}
						return err
					} else if st.Code() != codes.NotFound {
						writeErr := n.writeTerminationMessage(&JobActionResult{Result: false, Error: err.Error()})
						if writeErr != nil {
							logger.Infof("Failed to write termination message: %s", writeErr)
							return err
						}
						return err
					}
				}
			}
		*/
	default:
		return fmt.Errorf("unknown action value %v", n.Action)
	}

	logger.Info("Done.")
	return nil
}

func (n *JobOps) toStorageEventTypes(eventTypes []string) []string {
	storageTypes := make([]string, 0, len(eventTypes))
	for _, eventType := range eventTypes {
		storageTypes = append(storageTypes, storageEventTypes[eventType])
	}

	if len(storageTypes) == 0 {
		return append(storageTypes, "OBJECT_FINALIZE")
	}
	return storageTypes
}

func (n *JobOps) writeTerminationMessage(result *JobActionResult) error {
	// Always add the project regardless of what we did.
	result.ProjectId = n.Project
	m, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return ioutil.WriteFile("/dev/termination-log", m, 0644)
}
