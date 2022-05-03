package imageickcf

import (
	"bytes"
	"context"

	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"cloud.google.com/go/storage"

	"bufio"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"

	ico "github.com/Kodeworks/golang-image-ico"
	"github.com/chai2010/webp"
	"github.com/disintegration/imaging"
	// "golang.org/x/image/bmp"
	// "golang.org/x/image/tiff"
)

const (
	limitWidth          = 16000
	limitHeight         = 16000
	bucketName          = "BUCKET_NAME"
	optimizedFilePrefix = "optimize"
	cacheMaxAge         = 31536000
	contentTypeWebp     = "image/webp"
	contentTypeGif      = "image/gif"
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
		// "bmp",
		// "tiff",
		"mp4",
		"ico",
	}
)

type optimizeOption struct {
	Format   string
	IsReduce bool
	IsResize bool
	Width    int
	Height   int
	Blur     float64
}

func (option *optimizeOption) isEmpty() bool {
	return option.Format == "" &&
		!option.IsReduce &&
		!option.IsResize &&
		option.Width <= 0 &&
		option.Height <= 0 &&
		option.Blur <= 0
}

func init() {
	var err error
	storageClient, err = storage.NewClient(context.Background())

	if err != nil {
		log.Fatalf("storage.NewClient: %v", err)
	}

	bucket = storageClient.Bucket(bucketName)
}

func OptimizeImage(w http.ResponseWriter, r *http.Request) {
	imageName := strings.TrimPrefix(r.URL.Path, "/")
	query := r.URL.Query()
	format := query.Get("format")
	isOptimize, isOptimizeOk := strconv.ParseBool(query.Get("optimize"))
	isOptimizeSize, isOptimizeSizeOk := strconv.ParseBool(query.Get("optimizeSize"))
	width, _ := strconv.Atoi(query.Get("width"))
	height, _ := strconv.Atoi(query.Get("height"))

	blur, blurErr := strconv.ParseFloat(query.Get("blur"), 32)

	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("%v", r)
			w.Header().Set("Cache-Control", "public,no-cache")
			http.Error(w, msg, http.StatusInternalServerError)
		}
	}()

	defer func() {
		r := recover()
		fmt.Println(r)
	}()

	if !contains(allowedFormatList, format) {
		format = ""
	}

	option := &optimizeOption{
		Format:   format,
		IsReduce: isOptimize && isOptimizeOk == nil,
		IsResize: isOptimizeSize && isOptimizeSizeOk == nil,
		Width:    width,
		Height:   height,
		Blur:     blur,
	}

	originalImage := bucket.Object(imageName)
	attrs, err := originalImage.Attrs(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	isGif := attrs.ContentType == contentTypeGif
	originalImageReader, err := originalImage.NewReader(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Cache-Control", "public,max-age="+strconv.Itoa(cacheMaxAge))
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if option.isEmpty() || (isGif && option.IsReduce) {
		io.Copy(w, originalImageReader)
		return
	}

	var resultImageBuffer bytes.Buffer
	resultImageBufferWriter := bufio.NewWriter(&resultImageBuffer)
	resultBufferReader := bufio.NewReader(&resultImageBuffer)

	rForSize, _ := originalImage.NewReader(r.Context())
	originalImageWidth, originalImageHeight, err := getImageWidthHeight(r.Context(), rForSize)
	if originalImageWidth > limitWidth || originalImageHeight > limitHeight {
		io.Copy(w, originalImageReader)
		return
	}

	if isGif && option.Format == "mp4" {
		// temp diabled.
		// fileName := strings.Split(originalImage.ObjectName(), ".")[0]
		// err = gif2mp4(r.Context(), fileName, originalImageReader, w)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	} else if option.IsReduce {
		minWidth := int(math.Min(float64(1024), float64(originalImageWidth)))
		option.Width = minWidth
		option.Format = "webp"

	} else if option.IsResize {
		minWidth := int(math.Min(float64(1024), float64(originalImageWidth)))
		option.Width = minWidth
	}

	var tmp bytes.Buffer

	img, err := imaging.Decode(originalImageReader, imaging.AutoOrientation(true))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if blurErr == nil {
		img = imaging.Blur(img, blur)
	}

	var resizeImg image.Image
	if option.Width <= 0 {
		resizeImg = img
	} else {
		resizeImg = imaging.Resize(img, option.Width, option.Height, imaging.Lanczos)
	}

	var fileType FileType

	if option.Format != "" {
		fileType = getFileType(strings.ToLower(option.Format))
	} else {
		fileType = getFileTypeFromContentType(strings.ToLower(attrs.ContentType))
	}

	switch fileType {
	case JPG:
		err = jpeg.Encode(&tmp, resizeImg, nil)
	case PNG:
		err = png.Encode(&tmp, resizeImg)
	case WEBP:
		err = webp.Encode(&tmp, resizeImg, nil)
	case GIF:
		err = gif.Encode(&tmp, resizeImg, nil)
	case ICO:
		err = ico.Encode(&tmp, resizeImg)
	case BMP:
		// err = bmp.Encode(&tmp, resizeImg)
		fallthrough
	case TIFF:
		// err = tiff.Encode(&tmp, resizeImg, nil)
		fallthrough
	default:
		http.Error(w, "unknown file type", http.StatusInternalServerError)
		return
	}

	io.Copy(resultImageBufferWriter, &tmp)

	resultImageBufferWriter.Flush()
	// result := io.TeeReader(resultBufferReader, gcsFileWriter)
	io.Copy(w, resultBufferReader)
	return
}

func gif2mp4(
	ctx context.Context,
	fileName string,
	r io.Reader,
	w io.Writer,
) error {
	inputFileName := fmt.Sprintf("/tmp/%s.gif", fileName)
	outputFileName := fmt.Sprintf("/tmp/%s.mp4", fileName)

	var stderr bytes.Buffer

	gifFile, err := os.Create(inputFileName)
	if err != nil {
		return err
	}
	defer os.Remove(inputFileName)
	defer os.Remove(outputFileName)
	defer gifFile.Close()
	io.Copy(gifFile, r)

	// ffmpeg -i animated.gif -movflags faststart -pix_fmt yuv420p -vf "scale=trunc(iw/2)*2:trunc(ih/2)*2" video.mp4
	cmd := exec.Command("ffmpeg", "-i", gifFile.Name(), "-movflags", "faststart", "-pix_fmt", "yuv420p", "-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2", outputFileName)

	// cmd := exec.Command("ffmpeg", "-f", "image2pipe", "-i", "pipe:0", "-movflags", "faststart", "-pix_fmt", "yuv420p", "-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2", "-f", "h264", "pipe:1")
	// cmd.Stdout = w
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, stderr.String())
		return err
	}

	resultMp4File, err := os.Open(outputFileName)
	if err != nil {
		return err
	}

	io.Copy(w, resultMp4File)

	return nil
}

func getImageWidthHeight(
	ctx context.Context,
	r io.Reader,
) (int, int, error) {
	imgConfig, _, err := image.DecodeConfig(r)
	if err != nil {
		return 0, 0, err
	}

	return imgConfig.Width, imgConfig.Height, nil
}

func contains(arr []string, value string) bool {
	for _, item := range arr {
		if item == value {
			return true
		}
	}

	return false
}

type FileType int

const (
	PNG FileType = iota
	JPG
	GIF
	WEBP
	BMP
	TIFF
	ICO
	ERR
)

func getFileType(input string) FileType {
	switch input {
	case "jpg":
		fallthrough
	case "jpeg":
		return JPG
	case "gif":
		return GIF
	case "webp":
		return WEBP
	case "png":
		return PNG
	case "ico":
		return ICO
	case "bmp":
		fallthrough
		// return BMP
	case "tiff":
		fallthrough
		// return TIFF
	default:
		return ERR
	}
}

func getFileTypeFromContentType(contentType string) FileType {
	switch contentType {
	case "image/jpg":
		fallthrough
	case "image/jpeg":
		return JPG
	case "image/gif":
		return GIF
	case "image/png":
		return PNG
	case "image/ico":
		return ICO
	case "image/webp":
		return WEBP
	case "image/bmp":
		fallthrough
		// return BMP
	case "image/tiff":
		fallthrough
		// return TIFF
	default:
		return ERR
	}
}
