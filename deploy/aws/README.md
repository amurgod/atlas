# Atlas on AWS (EKS)

This overlay is meant to run the same Kubernetes manifests as local Kind, with four substitutions: images live in ECR, Postgres is RDS, the notification service sends mail through SES using IRSA, and open-webui-engine stores documents in S3 using IRSA.

```
React UI ──► ALB ──► gateway (nginx)
                       ├─ user / knowledge / planner / notify
                       ├─ open-webui-engine ── S3
                       └─ ui
Go services ── NATS JetStream ── notification ── SES / Twilio
Knowledge / planner ── llm-gateway ── Anthropic Messages, OpenAI-compatible Completions, Voyage
All stateful data except NATS and document bytes ── RDS Postgres 16 + pgvector
```

## 1. Prerequisites

- AWS CLI, Terraform >= 1.6, Docker, kubectl
- A Route 53 hosted zone (or any DNS you can point at an ALB)
- An ACM certificate in the same region as the cluster
- Anthropic, OpenAI-compatible Completions, and Voyage keys if you want live models (otherwise local fallbacks still run)

## 2. Provision infrastructure

```bash
cd deploy/aws/terraform
cp terraform.tfvars.example terraform.tfvars
# set db_password, region, cluster_name
terraform init
terraform apply
aws eks update-kubeconfig --region us-west-2 --name atlas
```

Install the AWS Load Balancer Controller in the cluster (once per cluster) following Amazon’s current Helm chart. The ingress object in `deploy/k8s/overlays/aws` expects `ingressClassName: alb`.

## 3. Push images

```bash
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
REGION=us-west-2
for svc in user knowledge planner notification llm-gateway open-webui-engine; do
  docker build --build-arg SERVICE=$svc -t atlas/$svc:local -f services/Dockerfile .
done
docker build -t atlas/ui:local ui

aws ecr get-login-password --region $REGION | docker login --username AWS --password-stdin $ACCOUNT.dkr.ecr.$REGION.amazonaws.com
for svc in user knowledge planner notification llm-gateway open-webui-engine ui; do
  docker tag atlas/$svc:local $ACCOUNT.dkr.ecr.$REGION.amazonaws.com/atlas-$svc:latest
  docker push $ACCOUNT.dkr.ecr.$REGION.amazonaws.com/atlas-$svc:latest
done
```

(`ui` uses `ui/Dockerfile`, not `services/Dockerfile`.)

## 4. Fill the overlay

1. Copy `deploy/k8s/overlays/aws/secrets.env.example` to `secrets.env`. It is gitignored, so it stays on your machine.
2. Set `DATABASE_URL` from `terraform output -raw database_url`.
3. Set `JWT_SECRET`, `ANTHROPIC_API_KEY`, `VOYAGE_API_KEY`, `SES_FROM`.
4. In `kustomization.yaml`, replace `ACCOUNT` in every ECR image name.
5. In `ingress-alb.yaml`, replace `CERTIFICATE_ARN`.
6. In `notify-ses-sa.yaml`, replace `IRSA_ROLE_ARN` with `terraform output -raw notification_role_arn`.
7. In `engine-serviceaccount.yaml`, replace `ENGINE_IRSA_ROLE_ARN` with `terraform output -raw engine_role_arn`.
8. In `s3-config.yaml`, replace `DOCS_BUCKET` with `terraform output -raw docs_bucket`.
9. Set the ingress host to your DNS name.

Verify SES identities (domain or `SES_FROM` address) in the same region. Until SES is out of sandbox, destination addresses must be verified too.

## 5. Apply the workload

```bash
kubectl apply -k deploy/k8s/overlays/aws
kubectl -n atlas rollout status deploy/user-service
```

On first boot, knowledge-service runs `CREATE EXTENSION IF NOT EXISTS vector`. RDS Postgres 16 includes pgvector; the connecting role needs permission to create extensions (the master user Terraform creates does).

## 6. What stays in-cluster on AWS

| Component | Where |
| --- | --- |
| Go services, UI, gateway | EKS |
| NATS JetStream | EKS (ephemeral `emptyDir`; swap for a PVC if you need durable replay across node death) |
| Postgres + pgvector | RDS |
| Document bytes | S3 via IRSA on `open-webui-engine` |
| Embeddings / completions | Voyage + Anthropic APIs via llm-gateway |
| Email | SES via IRSA on `notification` |
| SMS | Twilio if `NOTIFY_MODE=twilio` and Twilio env is set |

Do not expose `llm-gateway` on the ALB. Only the in-cluster gateway nginx speaks to it, and even then only the Go services do.
