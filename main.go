package imageickcf

import (
	"cloud.google.com/go/storage"
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

const (
	bucketName = "BUCKET_NAME"
)

var (
	proejctId     = os.Getenv("GOOGLE_CLOUD_PROJECT")
	storageClient *storage.Client
	bucket        *storage.BucketHandle
)

func init() {
	var err error
	storageClient, err = storage.NewClient(context.Background())

	if err != nil {
		log.Fatalf("storage.NewClient: %v", err)
	}

	bucket = storageClient.Bucket(bucketName)
}

func ReceiveHttp(w http.ResponseWriter, r *http.Request) {
	log.Println(r.URL.Path)
	name := strings.TrimPrefix(r.URL.Path, "/")
	existFileReader, err := bucket.Object(name).NewReader(context.Background())
	if err == nil {
		defer existFileReader.Close()
		io.Copy(w, existFileReader)
	} else {
		log.Fatal(err)
	}
}
