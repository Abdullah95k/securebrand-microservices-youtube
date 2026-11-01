.PHONY: check fmt fmt-check vet test fixturescan

# Run everything CI runs (except gitleaks, which CI downloads separately).
check: fmt-check vet test fixturescan

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "Run 'make fmt'. Not formatted:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

test:
	go test -race -count=1 ./...

fixturescan:
	go run ./cmd/fixturescan .
