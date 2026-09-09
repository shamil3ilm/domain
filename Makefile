# Makefile — convenience wrappers for common operations.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

.PHONY: help install up down logs ps build rebuild backup restore health test test-dns seed clean

help: ## show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

install: ## initial install (generates .env, builds, starts, verifies)
	./scripts/install.sh

up: ## start the stack
	docker compose up -d

down: ## stop the stack
	docker compose down

logs: ## tail all logs
	docker compose logs -f --tail=100

ps: ## service status
	docker compose ps

build: ## build all images
	docker compose build

rebuild: ## rebuild all images from scratch
	docker compose build --no-cache

backup: ## snapshot everything into ./backups/
	./scripts/backup.sh

restore: ## restore a backup: make restore ARCHIVE=backups/xxx.tar.gz
	./scripts/restore.sh "$(ARCHIVE)"

health: ## verify services + DNS
	./scripts/healthcheck.sh

test-dns: ## quick DNS smoke test
	./scripts/test-dns.sh

seed: ## create example zone + records for smoke testing
	./scripts/seed-example.sh

test: ## run Go unit tests
	cd api && go test ./...

clean: ## stop + remove all data volumes (DESTRUCTIVE)
	docker compose down -v
