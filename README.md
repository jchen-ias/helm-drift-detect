# Helm Drift Detect

Helm Drift Detect is a tool designed to detect drifts in Helm releases. It provides insights into discrepancies between the declared state in Helm and the actual state in the Kubernetes cluster.

## Requirements

- This script uses internal modules of the `helm-controller`, so it must be compiled within the `helm-controller` project.

## Note

- For simplicity, the script does not consider the ignore rules defined in `HelmRelease`. As a result, it outputs all detected drifts, including some that might otherwise be ignored.

## Usage

To use this tool, follow these steps:

1. Clone the `helm-controller` project.
2. Compile the `helm-drift-detect` script within the `helm-controller` project.
3. Execute the script to detect drifts in your Helm releases.

```shell
$ ./drift -n descheduler -r descheduler
Detected drift in HelmRelease descheduler/descheduler:

1 - Resource: Deployment/descheduler
    Reason: changed
    1 - Path: /spec/template/spec/containers/0/resources/requests
        Recovery Operation: replace
        Original Value: map[cpu:500m memory:256Mi]
```
