IMAGE ?= egate:latest
CONTAINER ?= egate
PORT ?= 11111

.PHONY: init docker docker-build docker-stop docker-logs

init: docker-build data
	@if [ ! -f .env ]; then \
		docker run --rm --user "$$(id -u):$$(id -g)" \
			-v "$(CURDIR):/workspace" \
			$(IMAGE) --init-env /workspace/.env; \
		echo "Created .env. Replace the starter credentials and Postmark token before deployment."; \
	fi

data:
	mkdir -p data

docker-build:
	docker build -t $(IMAGE) .

docker: init
	-docker rm -f $(CONTAINER) >/dev/null 2>&1
	docker run -d \
		--name $(CONTAINER) \
		--restart unless-stopped \
		--env-file .env \
		-p $(PORT):$(PORT) \
		-v "$(CURDIR)/.env:/app/.env:ro" \
		-v "$(CURDIR)/data:/app/data" \
		$(IMAGE)

docker-stop:
	-docker rm -f $(CONTAINER)

docker-logs:
	docker logs -f $(CONTAINER)
