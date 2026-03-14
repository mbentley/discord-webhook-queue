IMAGE     := discord-webhook-queue
GO_IMAGE  := golang:1.26-alpine

.PHONY: help mod-tidy build-dev build-prod run clean

help:
	@echo "Usage: make <target>"
	@echo ""
	@echo "Targets:"
	@echo "  mod-tidy    Generate go.sum (run once after cloning if go.sum is missing)"
	@echo "  build-dev   Build image for local dev/test (native ARM64)"
	@echo "  build-prod  Build image for production (cross-compile to linux/amd64)"
	@echo "  run         Build and run the container locally on port 8080"
	@echo "  clean       Remove built images and data volume"

# Generate go.sum — run this once after cloning if go.sum is missing.
mod-tidy:
	docker run --rm -v "$(PWD):/build" -w /build $(GO_IMAGE) go mod tidy

# Build for local dev/test — native build on ARM Mac, produces linux/arm64.
build-dev:
	docker build -t $(IMAGE):dev .

# Build for production deployment (cross-compile to linux/amd64).
build-prod:
	docker buildx build --platform linux/amd64 -t $(IMAGE):latest --load .

# Primary dev workflow: native build (arm64 on ARM Mac) and run.
run:
	docker build -t $(IMAGE):dev .
	docker run --rm \
		-p 8080:8080 \
		-v discord-webhook-queue-data:/data \
		--user 523:523 \
		--env-file .env \
		$(IMAGE):dev

clean:
	docker rmi $(IMAGE):dev $(IMAGE):latest 2>/dev/null || true
	docker volume rm discord-webhook-queue-data 2>/dev/null || true