.PHONY: test test-race lint coverage integration compose-config build swagger db-migrate db-verify db-replay db-shell db-backup

test:
	go test ./...

test-race:
	go test -race ./...

lint:
	golangci-lint run ./...

coverage:
	go test '-coverprofile=coverage.out' ./...
	go tool cover '-func=coverage.out'

integration:
	go test -tags integration ./tests/integration -v

compose-config:
	docker compose config --quiet

build:
	docker compose build order-sync

swagger:
	go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/order-sync/main.go -d . -o docs --parseInternal --parseDependency

db-migrate:
	docker compose run --rm --no-deps --entrypoint /app/order-sync-admin order-sync migrate

db-verify:
	docker compose run --rm --no-deps --entrypoint /app/order-sync-admin order-sync verify

db-replay:
	docker compose run --rm --no-deps --entrypoint /app/order-sync-admin order-sync replay $(JOB_ID)

db-shell:
	docker compose exec postgres sh -c 'psql -U "$$POSTGRES_USER" -d "$$POSTGRES_DB"'

db-backup:
	docker compose exec -T postgres sh -c 'pg_dump -U "$$POSTGRES_USER" -d "$$POSTGRES_DB"' > postgres-backup.sql
