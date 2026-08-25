FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /booking-orchestrator .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /booking-orchestrator /booking-orchestrator
EXPOSE 8080
ENTRYPOINT ["/booking-orchestrator"]
CMD ["serve"]
