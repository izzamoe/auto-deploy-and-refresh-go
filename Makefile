.PHONY: build build-arm64 vet clean

build:
	go build -o auto-deploy .

build-arm64:
	GOOS=linux GOARCH=arm64 go build -o auto-deploy-arm64 .

vet:
	go vet ./...

clean:
	rm -f auto-deploy auto-deploy-arm64
