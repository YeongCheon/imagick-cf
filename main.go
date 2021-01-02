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
	"math"
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
		log.Println(stderr.String())
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
	w io.Writer,
	fileType FileType,
) error {
	img, _, err := image.Decode(r)
	if err != nil {
		return err
	}

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
	width,
	height int,
) error {
	convertArgs := []string{}
	convertArgs = append(convertArgs, "-") // input stream

	widthStr := strconv.Itoa(width)
	heightStr := strconv.Itoa(height)
	if width > 0 && height <= 0 {
		convertArgs = append(convertArgs, "-resize", widthStr)
	} else if width > 0 && height > 0 {
		convertArgs = append(convertArgs, "-resize", widthStr+"x"+heightStr+"!")
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

func getImageWidth(
	ctx context.Context,
	r io.Reader,
) (int, error) {
	imgConfig, _, err := image.DecodeConfig(r)
	if err != nil {
		return 0, err
	}

	
	return imgConfig.Width, nil
}

func reduceImage(
	ctx context.Context,
	originalFile *storage.ObjectHandle,
	// r io.Reader,
	w io.Writer,
) error {
	const WIDTH = 1024

	rForSize, _  := originalFile.NewReader(ctx)
	width, err := getImageWidth(ctx, rForSize)
	if err != nil {
		return nil
	}
	
	minWidth := int(math.Min(float64(WIDTH), float64(width)))

	rForResize, _ := originalFile.NewReader(ctx)
	var resizeBuf bytes.Buffer
	err = imageResize(context.Background(), rForResize, &resizeBuf, minWidth, 0) // cloud function convert command is not support webp format.
	if err != nil {
		return err
	}

	return convertImage(ctx, &resizeBuf, w, WEBP) 
}

func OptimizeImage(w http.ResponseWriter, r *http.Request) {
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

	originalImage := bucket.Object(imageName)
	originalImageReader, err := originalImage.NewReader(context.Background())

	defer func() {
		if r := recover(); r != nil {
			io.Copy(w, originalImageReader)
		}
	}()

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

		if isGif {
			err = gif2mp4(context.Background(), originalImageReader, resultImageBufferWriter)
			
			defer existFileObject.Update(context.Background(), storage.ObjectAttrsToUpdate{
				ContentType: "video/mp4",
				ContentDisposition: "",
				// Metadata: metadata,
			})
		} else if option.IsReduce {
			err = reduceImage(context.Background(), originalImage, resultImageBufferWriter)
		} else {
			// result, err = imageProcess(context.Background(), imageName, optimizedFileName, option)
			var resizeImageBuffer bytes.Buffer
			resizeImageBufferWriter := bufio.NewWriter(&resizeImageBuffer)
			resizeBufferReader := bufio.NewReader(&resizeImageBuffer)

			err = imageResize(context.Background(), originalImageReader, resizeImageBufferWriter, option.Width, option.Height) // warning: this function must be first. if not, result buffer bytes size is zero.
			if err != nil {
				log.Fatal(err)
			}
			resizeImageBufferWriter.Flush()
			
			// var convertImageBuffer bytes.Buffer
			convertImageBuffer := bytes.NewBuffer([]byte{})
			convertImageBufferWriter := bufio.NewWriter(convertImageBuffer)
			convertBufferReader := bufio.NewReader(convertImageBuffer)


			fileType := getFileType(option.Format)
			err = convertImage(context.Background(), resizeBufferReader, convertImageBufferWriter, fileType)
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
