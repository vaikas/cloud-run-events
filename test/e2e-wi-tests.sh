#!/usr/bin/env bash

# Copyright 2020 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

source $(dirname "${BASH_SOURCE[0]}")/lib.sh

source $(dirname "${BASH_SOURCE[0]}")/../hack/lib.sh

source $(dirname "${BASH_SOURCE[0]}")/e2e-common.sh

readonly BROKER_SERVICE_ACCOUNT="broker"
readonly PROW_SERVICE_ACCOUNT_EMAIL=$(gcloud config get-value core/account)
# Constants used for creating ServiceAccount for Data Plane(Pub/Sub Admin) if it's not running on Prow.
readonly PUBSUB_SERVICE_ACCOUNT_NON_PROW_KEY_TEMP="$(mktemp)"
readonly CONFIG_GCP_AUTH="test/test_configs/config-gcp-auth-wi.yaml"
readonly K8S_SERVICE_ACCOUNT_NAME="ksa-name"

function export_variable() {
  readonly MEMBER="serviceAccount:${E2E_PROJECT_ID}.svc.id.goog[${CONTROL_PLANE_NAMESPACE}/${K8S_CONTROLLER_SERVICE_ACCOUNT}]"
  readonly BROKER_MEMBER="serviceAccount:${E2E_PROJECT_ID}.svc.id.goog[${CONTROL_PLANE_NAMESPACE}/${BROKER_SERVICE_ACCOUNT}]"
  if (( ! IS_PROW )); then
    readonly CONTROL_PLANE_SERVICE_ACCOUNT_EMAIL="${CONTROL_PLANE_SERVICE_ACCOUNT_NON_PROW}@${E2E_PROJECT_ID}.iam.gserviceaccount.com"
    readonly PUBSUB_SERVICE_ACCOUNT_EMAIL="${PUBSUB_SERVICE_ACCOUNT_NON_PROW}@${E2E_PROJECT_ID}.iam.gserviceaccount.com"
    readonly DATA_PLANE_SERVICE_ACCOUNT_EMAIL=PUBSUB_SERVICE_ACCOUNT_EMAIL
    readonly PUBSUB_SERVICE_ACCOUNT_KEY_TEMP="${PUBSUB_SERVICE_ACCOUNT_NON_PROW_KEY_TEMP}"
  else
    readonly CONTROL_PLANE_SERVICE_ACCOUNT_EMAIL=${PROW_SERVICE_ACCOUNT_EMAIL}
    # Get the PROW service account.
    readonly PROW_PROJECT_NAME=$(cut -d'.' -f1 <<< "$(cut -d'@' -f2 <<< "${PROW_SERVICE_ACCOUNT_EMAIL}")")
    readonly DATA_PLANE_SERVICE_ACCOUNT_EMAIL="cloud-run-events-source@${PROW_PROJECT_NAME}.iam.gserviceaccount.com"
    readonly PUBSUB_SERVICE_ACCOUNT_EMAIL=${PROW_SERVICE_ACCOUNT_EMAIL}
    readonly PUBSUB_SERVICE_ACCOUNT_KEY_TEMP="${GOOGLE_APPLICATION_CREDENTIALS}"
  fi
}

# Setup resources common to all eventing tests.
function test_setup() {
  pubsub_setup "workload_identity" || return 1

  # Authentication check test for BrokerCell. It is used in integration test in workload identity mode.
  # We do not put it in the same place as other integration tests, because this test can not run in parallel with others,
  # as this test requires the entire BrokerCell to be non-functional.
  if [[ -v ENABLE_AUTH_CHECK_TEST && $ENABLE_AUTH_CHECK_TEST == "true" ]]; then
    test_authentication_check_for_brokercell "workload_identity" || return 1
  fi

  gcp_broker_setup "workload_identity" || return 1
  # Create private key that will be used in storage_setup
  create_private_key_for_pubsub_service_account || return 1
  storage_setup || return 1
  scheduler_setup || return 1
  echo "Sleep 2 mins to wait for all resources to setup"
  sleep 120

  # Publish test images.
  publish_test_images
}

function control_plane_setup() {
  # When not running on Prow we need to set up a service account for managing resources.
  if (( ! IS_PROW )); then
    echo "Set up ServiceAccount used by the Control Plane"
    init_control_plane_service_account "${E2E_PROJECT_ID}" "${CONTROL_PLANE_SERVICE_ACCOUNT_NON_PROW}"
    local cluster_name="$(cut -d'_' -f4 <<<"$(kubectl config current-context)")"
    local cluster_location="$(cut -d'_' -f3 <<<"$(kubectl config current-context)")"
    enable_workload_identity "${E2E_PROJECT_ID}" "${CONTROL_PLANE_SERVICE_ACCOUNT_NON_PROW}" "${cluster_name}" "${cluster_location}" "${REGIONAL_CLUSTER_LOCATION_TYPE}"
    gcloud iam service-accounts add-iam-policy-binding \
      --role roles/iam.workloadIdentityUser \
      --member "${MEMBER}" "${CONTROL_PLANE_SERVICE_ACCOUNT_EMAIL}"
    kubectl annotate --overwrite serviceaccount "${K8S_CONTROLLER_SERVICE_ACCOUNT}" iam.gke.io/gcp-service-account="${CONTROL_PLANE_SERVICE_ACCOUNT_EMAIL}" \
      --namespace "${CONTROL_PLANE_NAMESPACE}"
    # Setup default credential information for Workload Identity.
    sed "s/K8S_SERVICE_ACCOUNT_NAME/${K8S_SERVICE_ACCOUNT_NAME}/g; s/PUBSUB-SERVICE-ACCOUNT/${DATA_PLANE_SERVICE_ACCOUNT_EMAIL}/g" ${CONFIG_GCP_AUTH} | ko apply -f -
  else
    prow_control_plane_setup "workload_identity"
  fi
  wait_until_pods_running "${CONTROL_PLANE_NAMESPACE}" || return 1
}

function create_private_key_for_pubsub_service_account {
  if (( ! IS_PROW )); then
    gcloud iam service-accounts keys create "${PUBSUB_SERVICE_ACCOUNT_KEY_TEMP}" \
      --iam-account="${PUBSUB_SERVICE_ACCOUNT_EMAIL}"
  fi
}


if [ "${SKIP_TESTS:-}" == "true" ]; then
  echo "**************************************"
  echo "***         TESTS SKIPPED          ***"
  echo "**************************************"
  exit 0
fi

# Create a cluster with Workload Identity enabled.
# We could specify --version to force the cluster using a particular GKE version.
initialize "$@" --enable-workload-identity=true

# Channel related e2e tests we have in Eventing is not running here.
go_test_e2e -timeout=30m -parallel=6 ./test/e2e -workloadIdentity=true -serviceAccountName="${K8S_SERVICE_ACCOUNT_NAME}" || fail_test

success
