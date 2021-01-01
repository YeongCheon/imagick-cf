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

	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"github.com/chai2010/webp"
	"golang.org/x/image/tiff"
	"golang.org/x/image/bmp"
	"bufio"
	"errors"
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
		"bmp",
		"tiff",
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

// deprecated
func gif2mp4(
	ctx context.Context,
	r io.Reader,
	w io.Writer,
) error {
	var stderr bytes.Buffer

	cmd := exec.Command("ffmpeg", "-f", "image2pipe", "-i", "pipe:0", "-movflags", "faststart", "-pix_fmt", "yuv420p", "-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2", "-f", "h264", "pipe:1")
	cmd.Stdin = r
	cmd.Stdout = w
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		log.Println(stderr.String())
		return err
	}

	return nil
}


// original: https://github.com/dawnlabs/photosorcery/blob/master/convert.go
func convertImage(
	ctx context.Context,
	r io.Reader,
	w io.Writer,
	optimizeOption *optimizeOption,
) error {
	img, _, err := image.Decode(r)
	if err != nil {
		return err
	}

	fileType := getFileType(optimizeOption.Format)
	switch fileType {
	case JPG:
		return jpeg.Encode(w, img, nil)
	case PNG:
		return png.Encode(w, img)
	case WEBP:
		return webp.Encode(w, img, nil)
	case GIF:
		return gif.Encode(w, img, nil)
	case BMP:
		return bmp.Encode(w, img)
	case TIFF:
		return tiff.Encode(w, img, nil)
	default:
		return errors.New("unknown file type")
	}
}

func imageResize(
	ctx context.Context,
	r io.Reader,
	w io.Writer,
	optimizeOption *optimizeOption,
) error {
	convertArgs := []string{}
	convertArgs = append(convertArgs, "-") // input stream

	width := strconv.Itoa(optimizeOption.Width)
	height := strconv.Itoa(optimizeOption.Height)

	if optimizeOption.Width > 0 && optimizeOption.Height <= 0 {
		convertArgs = append(convertArgs, "-resize", width)
	} else if optimizeOption.Width > 0 && optimizeOption.Height > 0 {
		convertArgs = append(convertArgs, "-resize", width+"x"+height+"!")
	}

	convertArgs = append(convertArgs, "-") // output stream

	var stderr bytes.Buffer
	// Use - as input and output to use stdin and stdout.
	cmd := exec.Command("convert", convertArgs...)
	cmd.Stdin = r
	cmd.Stdout = w
	cmd.Stderr = &stderr
	
	if err := cmd.Run(); err != nil {
		log.Println(stderr.String())
		return err
	}

	return nil
}

func imageProcess( //using imagick
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
	convertArgs = append(convertArgs, "-") // input stream
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
		convertArgs = append(convertArgs, optimizeOption.Format+":-")
	} else {
		convertArgs = append(convertArgs, "-")
	}

	var stderr bytes.Buffer
	// Use - as input and output to use stdin and stdout.
	cmd := exec.Command("convert", convertArgs...)
	cmd.Stdin = r
	cmd.Stdout = w
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		log.Println(stderr.String())
		return nil, err
	}

	return resultImage, nil
}

func optimizeImage(
	ctx context.Context,
	r io.Reader,
	w io.Writer,
) error {	
	const WIDTH = 1024

	img, _, err := image.Decode(r)
	if err != nil {
		return err
	}

	var webpBuf bytes.Buffer
	err = webp.Encode(&webpBuf, img, nil)
	if err != nil {
		return err
	}

	bounds := img.Bounds()
	width := bounds.Max.X
	// height := bounds.Max.Y

	convertArgs := []string{}
	convertArgs = append(convertArgs, "-") // input stream
	convertArgs = append(convertArgs, "-strip", "-interlace", "Plane", "-gaussian-blur", "0.05", "-quality", "85%")
	if width > 1024 {
		convertArgs = append(convertArgs, "-resize", strconv.Itoa(WIDTH))
	}
	convertArgs = append(convertArgs, "-") // output stream

	var stderr bytes.Buffer
	// Use - as input and output to use stdin and stdout.
	cmd := exec.Command("convert", convertArgs...)
	cmd.Stdin = &webpBuf
	cmd.Stdout = w
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		log.Println(stderr.String())
		return err
	}

	return nil
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

	existFileObject := bucket.Object(optimizedFileName)
	existFileReader, err := existFileObject.NewReader(context.Background())
	if err == nil {
		defer existFileReader.Close()
		io.Copy(w, existFileReader)
	} else {
		originalImage := bucket.Object(imageName)
		
		originalImageReader, err := originalImage.NewReader(context.Background())
		if err != nil {
			log.Fatal(err)
		}

		var resultImageBuffer bytes.Buffer
		resultImageBufferWriter := bufio.NewWriter(&resultImageBuffer)
		resultBufferReader := bufio.NewReader(&resultImageBuffer)

		if option.IsReduce {
			err = optimizeImage(context.Background(), originalImageReader, resultImageBufferWriter)
		} else if isGif {
			err = gif2mp4(context.Background(), originalImageReader, resultImageBufferWriter)

			defer existFileObject.Update(context.Background(), storage.ObjectAttrsToUpdate{
				ContentType: "video/mp4",
				ContentDisposition: "",
				// Metadata: metadata,
			})
		} else {
			// result, err = imageProcess(context.Background(), imageName, optimizedFileName, option)
			var resizeImageBuffer bytes.Buffer
			resizeImageBufferWriter := bufio.NewWriter(&resizeImageBuffer)
			resizeBufferReader := bufio.NewReader(&resizeImageBuffer)

			err = imageResize(context.Background(), originalImageReader, resizeImageBufferWriter, option) // warning: this function must be first. if not, result buffer bytes size is zero.
			if err != nil {
				log.Fatal(err)
			}
			resizeImageBufferWriter.Flush()
			
			// var convertImageBuffer bytes.Buffer
			convertImageBuffer := bytes.NewBuffer([]byte{})
			convertImageBufferWriter := bufio.NewWriter(convertImageBuffer)
			convertBufferReader := bufio.NewReader(convertImageBuffer)

			
			err = convertImage(context.Background(), resizeBufferReader, convertImageBufferWriter, option)
			if err != nil {
				log.Fatal(err)
			}
			convertImageBufferWriter.Flush()
			
			io.Copy(resultImageBufferWriter, convertBufferReader)
		}		
		
		if err != nil {
			log.Fatal(err)
		}

		gcsFileWriter := existFileObject.NewWriter(context.Background())
		defer gcsFileWriter.Close()

		resultImageBufferWriter.Flush()
		result := io.TeeReader(resultBufferReader, gcsFileWriter)
		io.Copy(w, result)
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
