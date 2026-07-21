.PHONY: dev-up dev-build dev-deploy dev-forward dev-smoke dev-reset dev-down

COMPONENT ?= all

dev-up:
	./deploy/scripts/local-dev.sh up

dev-build:
	./deploy/scripts/local-dev.sh build "$(COMPONENT)"

dev-deploy:
	./deploy/scripts/local-dev.sh deploy

dev-forward:
	./deploy/scripts/local-dev.sh forward

dev-smoke:
	./deploy/scripts/local-dev.sh smoke

dev-reset:
	./deploy/scripts/local-dev.sh reset

dev-down:
	./deploy/scripts/local-dev.sh down
