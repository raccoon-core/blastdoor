# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build

WORKDIR /src

# Cached separately so dependency downloads survive source-only changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
	-ldflags="-s -w -X github.com/raccoon-core/blastdoor/internal/cli.Version=${VERSION}" \
	-o /out/blastdoor ./cmd/blastdoor

FROM alpine:3.22

# git for change detection, the rest for tenv's downloads and signature checks.
RUN apk add --no-cache ca-certificates git bash curl unzip cosign

# tenv resolves and installs OpenTofu, Terraform and Terragrunt versions from
# the version files in each unit.
ARG TENV_VERSION=v4.15.1
ARG TARGETARCH=amd64
RUN curl -fsSL -o /tmp/tenv.apk \
	"https://github.com/tofuutils/tenv/releases/download/${TENV_VERSION}/tenv_${TENV_VERSION}_${TARGETARCH}.apk" \
	&& apk add --no-cache --allow-untrusted /tmp/tenv.apk \
	&& rm /tmp/tenv.apk

# The apk drops tofu/terraform/terragrunt shims into /usr/bin already; this
# just lets them fetch whatever version a repo asks for instead of failing.
ENV TENV_AUTO_INSTALL=true

COPY --from=build /out/blastdoor /usr/local/bin/blastdoor

WORKDIR /work
ENTRYPOINT ["blastdoor"]
CMD ["--help"]
