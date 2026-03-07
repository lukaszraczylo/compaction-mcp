BINARY := compactor
BUILDFLAGS := -buildvcs=false -trimpath

.PHONY: build clean test lint

build:
	go build $(BUILDFLAGS) -o $(BINARY) .

clean:
	rm -f $(BINARY)

test:
	go test -race -count=1 ./...

lint:
	go vet ./...
	gofmt -l .
