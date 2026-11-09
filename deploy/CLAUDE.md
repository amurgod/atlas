# Deployment and infrastructure

These rules also cover `docker-compose.yml`, `services/Dockerfile`, and `ui/Dockerfile`.

## Commands (run from the repo root)

```bash
make images                                      # build all images, tagged atlas/<name>:local
make kustomize-local                             # apply the local overlay
kubectl kustomize deploy/k8s/overlays/local      # render without applying
kubectl apply -f deploy/k8s/examples/providers.yaml   # sample Providers; set spec.apiKey on the one used
docker compose config --quiet                    # validate Compose
cd deploy/aws/terraform && terraform fmt -check && terraform init && terraform validate
```

Terraform requires >= 1.6. Whole-stack run commands are in the root file.

## Layout

| Path | Purpose |
|---|---|
| `deploy/k8s/base` | Workloads, NATS, nginx gateway, ingress, Provider CRD and RBAC |
| `deploy/k8s/overlays/local` | Kind: adds Postgres and MinIO, removes the ingress host, dev secrets |
| `deploy/k8s/overlays/aws` | EKS: ECR images, ALB ingress, SES, replicas, `secrets.env` |
| `deploy/aws/terraform` | EKS, RDS Postgres 16, ECR, S3 document bucket, IRSA roles for SES and S3 |
| `deploy/kind/cluster.yaml` | Kind cluster, ingress on `:18082` and `:18443` |

## Conventions

- **Config**: non-secret values in the `atlas-config` ConfigMap, secrets in the `atlas-secrets` Secret, both loaded with `envFrom`.
- **Names**: namespace `atlas`. Deployments and in-cluster DNS names use `<name>-service` for `user`, `knowledge`, `planner`, and `notification`. Compose matches those names with network aliases.
- **Workloads**: every Deployment sets resource limits and a readiness probe.
- **Images**: app services share `services/Dockerfile` through `--build-arg SERVICE=<name>` and run as non-root UID 65532.
- **Providers**: `atlas.platform/v1` custom resources read by `llm-gateway`. Example files keep `apiKey` empty.
- **Kind loading**: load one image at a time, since `kind load` of several can fail on attestation blobs. Third-party multi-platform images use `docker save IMAGE | docker exec -i atlas-control-plane ctr -n k8s.io images import -`.

## Rules

- **Secrets**: real values never enter git. `secrets.env` and `*.tfvars` are gitignored, and only their `.example` files are tracked. Terraform secrets are `sensitive` variables.
- **Provider keys**: applied to the cluster only, never committed.
- **Pinned versions**: every image and module has an explicit version. Never add `:latest` or a moving branch tag.
- **Least privilege**: narrowest RBAC verbs and IAM actions, as with the read-only `llm-gateway-providers` Role. Each workload that calls an API gets its own ServiceAccount.
- **Internal stays internal**: `llm-gateway`, Postgres, NATS, and MinIO are never added to the ingress.
- **Overlays**: environment differences go in an overlay, and `base` stays environment-neutral.
- **Confirm first**: ask before `terraform apply` or `destroy`, or any `kubectl apply` against AWS. RDS has `deletion_protection = false` and `skip_final_snapshot = true`, so a destroy loses the database.

## Verify before done

Run only what applies to the change:

1. Kubernetes: `kubectl kustomize deploy/k8s/overlays/local` succeeds, and so does `deploy/k8s/overlays/aws` (needs a local `secrets.env`, copied from `secrets.env.example`).
2. Compose: `docker compose config --quiet` succeeds.
3. Terraform: `terraform fmt -check` and `terraform validate` pass.
4. Gateway: `diff deploy/gateway/default.conf deploy/k8s/base/gateway-default.conf` prints nothing.
5. Dockerfiles: `make images` succeeds.

## Suggested improvements

- Unpinned: `open-webui:main`, `chainguard/minio:latest`, and the AWS overlay's `newTag: latest`.
- The Open WebUI container shares the `open-webui-engine` pod, so it also receives that ServiceAccount's S3 access. Splitting them would narrow it.
- App workloads set no `securityContext`, and there is no `NetworkPolicy` restricting access to `llm-gateway`.
- SES IAM policy uses `resources = ["*"]`, and the EKS API endpoint is public.
