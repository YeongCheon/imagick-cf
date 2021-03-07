package imageickcf

import (
	"bytes"
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

	"cloud.google.com/go/storage"

	"bufio"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"

	"github.com/chai2010/webp"
	"github.com/disintegration/imaging"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
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
		"bmp",
		"tiff",
		"mp4",
	}
)

type optimizeOption struct {
	Format   string
	IsReduce bool
	IsResize bool
	Width    int
	Height   int
}

func (option *optimizeOption) isEmpty() bool {
	return option.Format == "" &&
		!option.IsReduce &&
		!option.IsResize &&
		option.Width <= 0 &&
		option.Height <= 0
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
		return option.getHash(originalFileName) + ".mp4"
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

func OptimizeImage(w http.ResponseWriter, r *http.Request) {
	imageName := strings.TrimPrefix(r.URL.Path, "/")
	query := r.URL.Query()
	format := query.Get("format")
	isOptimize, isOptimizeOk := strconv.ParseBool(query.Get("optimize"))
	isOptimizeSize, isOptimizeSizeOk := strconv.ParseBool(query.Get("optimizeSize"))
	width, _ := strconv.Atoi(query.Get("width"))
	height, _ := strconv.Atoi(query.Get("height"))

	if !contains(allowedFormatList, format) {
		format = ""
	}

	option := &optimizeOption{
		Format:   format,
		IsReduce: isOptimize && isOptimizeOk == nil,
		IsResize: isOptimizeSize && isOptimizeSizeOk == nil,
		Width:    width,
		Height:   height,
	}

	originalImage := bucket.Object(imageName)
	attrs, err := originalImage.Attrs(r.Context())
	if err != nil {
		panic(err)
	}
	isGif := attrs.ContentType == contentTypeGif
	originalImageReader, err := originalImage.NewReader(r.Context())
	if err != nil {
		panic(err)
	}

	w.Header().Set("Cache-Control", "public,max-age="+strconv.Itoa(cacheMaxAge))

	if option.isEmpty() || (isGif && option.IsReduce) {
		io.Copy(w, originalImageReader)
		return
	}

	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("%v", r)
			http.Error(w, msg, http.StatusBadRequest)
			io.Copy(w, originalImageReader)
		}
	}()

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
		fmt.Println("gif2mp4")
		err = gif2mp4(r.Context(), originalImageReader, w)
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
		panic(err)
	}

	resizeImg := imaging.Resize(img, option.Width, option.Height, imaging.Lanczos)
	var fileType FileType

	if option.Format != "" {
		fileType = getFileType(option.Format)
	} else {
		fileType = getFileTypeFromContentType(attrs.ContentType)
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
	case BMP:
		err = bmp.Encode(&tmp, resizeImg)
	case TIFF:
		err = tiff.Encode(&tmp, resizeImg, nil)
	default:
		panic("unknown file type")
	}

	io.Copy(resultImageBufferWriter, &tmp)

	resultImageBufferWriter.Flush()
	// result := io.TeeReader(resultBufferReader, gcsFileWriter)
	io.Copy(w, resultBufferReader)
}

func gif2mp4(
	ctx context.Context,
	r io.Reader,
	w io.Writer,
) error {
	var stderr bytes.Buffer

	gifFile, err := os.Create("/tmp/tmpGifFile.gif")
	if err != nil {
		return err
	}
	defer gifFile.Close()
	io.Copy(gifFile, r)

	outputFileName := "/tmp/resultMp4File.mp4"

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

// original: https://github.com/dawnlabs/photosorcery/blob/master/convert.go
func convertImage(
	ctx context.Context,
	r io.Reader,
	fileType FileType,
) (*bytes.Buffer, error) {
	img, _, err := image.Decode(r)
	if err != nil {
		return nil, err
	}

	var w bytes.Buffer

	switch fileType {
	case JPG:
		jpeg.Encode(&w, img, nil)
	case PNG:
		png.Encode(&w, img)
	case WEBP:
		webp.Encode(&w, img, nil)
	case GIF:
		gif.Encode(&w, img, nil)
	case BMP:
		bmp.Encode(&w, img)
	case TIFF:
		tiff.Encode(&w, img, nil)
	default:
		return nil, errors.New("unknown file type")
	}

	return &w, nil
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
	case "bmp":
		return BMP
	case "webp":
		return WEBP
	case "tiff":
		return TIFF
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
	case "image/bmp":
		return BMP
	case "image/webp":
		return WEBP
	case "image/tiff":
		return TIFF
	default:
		return ERR
	}
}
