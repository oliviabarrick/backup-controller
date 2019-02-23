.PHONY: test build release

%-release:
	@echo -e "Release $$(git semver --dryrun $*):\n" > /tmp/CHANGELOG
	@echo -e "$$(git log --pretty=format:"%h (%an): %s" $$(git describe --tags --abbrev=0 @^)..@)\n" >> /tmp/CHANGELOG
	@cat /tmp/CHANGELOG CHANGELOG > /tmp/NEW_CHANGELOG
	@mv /tmp/NEW_CHANGELOG CHANGELOG

	@sed -i 's#image: justinbarrick/backup-controller:.*#image: justinbarrick/backup-controller:$(shell git semver --dryrun $*)#g' deploy.yaml

	@git add CHANGELOG deploy.yaml
	@git commit -m "Release $(shell git semver --dryrun $*)"
	@git semver $*

test:
	test -z $(shell gofmt -l .)
	go test

build:
	gofmt -w .
	CGO_ENABLED=0 go build -ldflags '-w -s -extldflags "-static"'
