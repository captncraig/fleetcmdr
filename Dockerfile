FROM golang:1.22

WORKDIR /app
 
COPY . .
 
RUN go mod download
 
RUN go build -o /fleetcmdr

ENTRYPOINT ["/fleetcmdr"]