package imageickcf

import (
	"cloud.google.com/go/storage"
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"bytes"
)

const (
	bucketName          = "BUCKET_NAME"
	optimizedFilePrefix = "optimize"
)

var (
	proejctId         = os.Getenv("GOOGLE_CLOUD_PROJECT")
	storageClient     *storage.Client
	bucket            *storage.BucketHandle
	allowedFormatList = []string{
		"jpg",
		"jpeg",
		"gif",
		"png",
		"webp",
	}
)

type optimizeOption struct {
	Format   string
	IsReduce bool
	Width    int
	Height   int
}

func (option *optimizeOption) getHash(originalFileName string) string {
	h := sha1.New()

	s := strings.Join([]string{
		originalFileName,
		strconv.FormatBool(option.IsReduce),
		option.Format,
		strconv.Itoa(option.Width),
		strconv.Itoa(option.Height),
	},
		"",
	)

	h.Write([]byte(s))

	return fmt.Sprintf("%x", h.Sum(nil))
}

func (option *optimizeOption) getFilename(originalFileName string) string {
	var result string
	if strings.HasSuffix(strings.ToLower(originalFileName), "gif") {
		return option.getHash(originalFileName)+".mp4"
	}
	
	if option.Format == "" {
		arr := strings.Split(originalFileName, ".")
		if len(arr) > 1 {
			result = option.getHash(originalFileName) + "." + arr[len(arr)-1]
		} else {
			result = option.getHash(originalFileName)
		}
	} else {
		result = option.getHash(originalFileName) + "." + option.Format
	}

	return result
}

func init() {
	var err error
	storageClient, err = storage.NewClient(context.Background())

	if err != nil {
		log.Fatalf("storage.NewClient: %v", err)
	}

	bucket = storageClient.Bucket(bucketName)
}

func gif2mp4(
	ctx context.Context,
	originalImageName string,
	outputImageName string,
) (*storage.ObjectHandle, error) {
	originalImage := bucket.Object(originalImageName)
	r, err := originalImage.NewReader(context.Background())
	if err != nil {
		return nil, err
	}

	resultImage := bucket.Object(outputImageName)	
	w := resultImage.NewWriter(ctx)
	defer resultImage.Update(context.Background(), storage.ObjectAttrsToUpdate{
		ContentType: "video/mp4",
		ContentDisposition: "",
		// Metadata: metadata,
	})
	defer w.Close()
	var stderr bytes.Buffer

	cmd := exec.Command("./ffmpeg", "-f", "image2pipe", "-i", "pipe:0", "-movflags", "faststart", "-pix_fmt", "yuv420p", "-c:a", "aac", "-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2", "-f", "ismv", "pipe:1")
	cmd.Stdin = r
	cmd.Stdout = w
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, err
	}

	return resultImage, nil
}

func imageProcess(
	ctx context.Context,
	originalImageName,
	outputImageName string,
	optimizeOption *optimizeOption,
) (*storage.ObjectHandle, error) {
	originalImage := bucket.Object(originalImageName)
	r, err := originalImage.NewReader(context.Background())
	if err != nil {
		return nil, err
	}

	resultImage := bucket.Object(outputImageName)
	w := resultImage.NewWriter(ctx)
	defer w.Close()

	convertArgs := []string{}
	if optimizeOption.IsReduce {
		convertArgs = append(convertArgs, "-strip", "-interlace", "Plane", "-gaussian-blur", "0.05", "-quality", "85%")
	}

	width := strconv.Itoa(optimizeOption.Width)
	height := strconv.Itoa(optimizeOption.Height)
	if optimizeOption.Width > 0 && optimizeOption.Height <= 0 {
		convertArgs = append(convertArgs, "-resize", width)
	} else if optimizeOption.Width > 0 && optimizeOption.Height > 0 {
		convertArgs = append(convertArgs, "-resize", width+"x"+height+"!")
	}

	if optimizeOption.Format != "" {
		convertArgs = append(convertArgs, "-", optimizeOption.Format+":-")
	} else {
		convertArgs = append(convertArgs, "-", "-")
	}

	// Use - as input and output to use stdin and stdout.
	cmd := exec.Command("convert", convertArgs...)
	cmd.Stdin = r
	cmd.Stdout = w

	if err := cmd.Run(); err != nil {
		return nil, err
	}

	return resultImage, nil
}

func ReceiveHttp(w http.ResponseWriter, r *http.Request) {
	imageName := strings.TrimPrefix(r.URL.Path, "/")
	query := r.URL.Query()
	format := query.Get("format")
	isOptimize, isOk := strconv.ParseBool(query.Get("optimize"))
	width, _ := strconv.Atoi(query.Get("width"))
	height, _ := strconv.Atoi(query.Get("height"))

	isGif := false
	if strings.HasSuffix(strings.ToLower(imageName), "gif") {
		isGif = true
	}

	if !contains(allowedFormatList, format) {
		format = ""
	}

	if strings.HasSuffix(strings.ToLower(imageName), "gif") {
		format = "mp4"
	}

	option := &optimizeOption{
		Format:   format,
		IsReduce: isOptimize && isOk == nil,
		Width:    width,
		Height:   height,
	}

	optimizedFileName := strings.Join([]string{optimizedFilePrefix, option.getFilename(imageName)}, "/")

	existFileReader, err := bucket.Object(optimizedFileName).NewReader(context.Background())
	if err == nil {
		defer existFileReader.Close()
		io.Copy(w, existFileReader)
	} else {
		var result *storage.ObjectHandle
		var err error
		if isGif {
			result, err = gif2mp4(context.Background(), imageName, optimizedFileName)
		} else {
			result, err = imageProcess(context.Background(), imageName, optimizedFileName, option)
		}		
		
		if err != nil {
			log.Fatal(err)
		}
		
		resultReader, _ := result.NewReader(context.Background())
		defer resultReader.Close()

		io.Copy(w, resultReader)
	}
}

func contains(arr []string, value string) bool {
	for _, item := range arr {
		if item == value {
			return true
		}
	}

	return false
}
