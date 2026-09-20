.PHONY: test vet workflowcheck temporal worker server lambda lambda-api terraform-init terraform-plan terraform-apply clean

test:
	go test ./...

vet:
	go vet ./...

workflowcheck:
	go run go.temporal.io/sdk/contrib/tools/workflowcheck@v0.5.0 ./...

temporal:
	mkdir -p .temporal
	temporal server start-dev --db-filename .temporal/temporal.db

worker:
	go run ./cmd/worker

server:
	go run ./cmd/server

LAMBDA_ARCH ?= arm64
lambda:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LAMBDA_ARCH) go build -tags lambda.norpc -o dist/bootstrap ./cmd/lambda-worker
	zip -j dist/temporal-word-game-worker.zip dist/bootstrap

lambda-api:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LAMBDA_ARCH) go build -tags lambda.norpc -o dist/api-bootstrap ./cmd/lambda-api
	zip -j dist/temporal-word-game-api.zip dist/api-bootstrap

terraform-init:
	terraform -chdir=deploy/terraform init

terraform-plan:
	terraform -chdir=deploy/terraform plan

terraform-apply:
	terraform -chdir=deploy/terraform apply

clean:
	go clean
