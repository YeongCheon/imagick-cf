package imageickcf

import (
	"cloud.google.com/go/storage"
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

const (
	bucketName          = "BUCKET_NAME"
	optimizedFilePrefix = "optimize"
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

func imageProcess(
	ctx context.Context,
	originalImageName,
	outputImageName string,
) (*storage.ObjectHandle, error) {
	originalImage := bucket.Object(originalImageName)
	r, err := originalImage.NewReader(context.Background())
	if err != nil {
		return nil, err
	}

	resultImage := bucket.Object(outputImageName)
	w := resultImage.NewWriter(ctx)
	defer w.Close()

	// Use - as input and output to use stdin and stdout.
	cmd := exec.Command("convert", "-strip", "-interlace", "Plane", "-gaussian-blur", "0.05", "-quality", "85%", "-", "-")
	cmd.Stdin = r
	cmd.Stdout = w

	if err := cmd.Run(); err != nil {
		return nil, err
	}

	return resultImage, nil
}

func ReceiveHttp(w http.ResponseWriter, r *http.Request) {
	log.Println(r.URL.Path)
	imageName := strings.TrimPrefix(r.URL.Path, "/")
	optimizedFileName := strings.Join([]string{optimizedFilePrefix, imageName}, "/")

	existFileReader, err := bucket.Object(optimizedFileName).NewReader(context.Background())
	if err == nil {
		defer existFileReader.Close()
		io.Copy(w, existFileReader)
	} else {
		result, err := imageProcess(context.Background(), imageName, optimizedFileName)
		if err != nil {
			log.Fatal(err)
		}
		resultReader, _ := result.NewReader(context.Background())
		defer resultReader.Close()

		io.Copy(w, resultReader)
	}
}
