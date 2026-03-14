IMAGE     := discord-queue
GO_IMAGE  := golang:1.26-alpine

.PHONY: mod-tidy build-dev build-prod run clean

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
		-v discord-queue-data:/data \
		--env-file .env \
		$(IMAGE):dev

clean:
	docker rmi $(IMAGE):dev $(IMAGE):latest 2>/dev/null || true
	docker volume rm discord-queue-data 2>/dev/null || true