DOCKER_REPO = localhost:5005/seminar/xds
DOCKER_TAG = latest

docker: ## Builds docker image
	docker buildx build -t $(DOCKER_REPO):$(DOCKER_TAG) .
