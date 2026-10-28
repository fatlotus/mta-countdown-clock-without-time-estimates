FROM golang:1.23-alpine AS builder
ARG COMMIT_SHA=""
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-X main.gitCommit=${COMMIT_SHA}" -o /app/mta-countdown .

FROM alpine:3.20
ARG COMMIT_SHA=""
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/mta-countdown /app/mta-countdown
COPY data /app/data
COPY web /app/web
ENV PORT=8080
ENV COMMIT_SHA=${COMMIT_SHA}
EXPOSE 8080
CMD ["/app/mta-countdown"]
