package istore

/*

#cgo pkg-config: libavformat

#include "libavformat/avio.h"

*/
import "C"

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	"github.com/golang/glog"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/umitanuki/gmf"
)

var (
	AVSEEK_SIZE = C.AVSEEK_SIZE
)

func selfURL(p string) string {
	r := strings.NewReplacer("?", "%3F", "%", "%25")
	return "self://" + r.Replace(p)
}

// HLine draws a horizontal line
func HLine(img draw.Image, x1, y, x2 int, col color.Color) {
	for ; x1 <= x2; x1++ {
		img.Set(x1, y, col)
	}
}

// VLine draws a veritcal line
func VLine(img draw.Image, x, y1, y2 int, col color.Color) {
	for ; y1 <= y2; y1++ {
		img.Set(x, y1, col)
	}
}

// Rect draws a rectangle utilizing HLine() and VLine()
func RectLine(img draw.Image, x1, y1, x2, y2 int, col color.Color) {
	HLine(img, x1, y1, x2, col)
	HLine(img, x1, y2, x2, col)
	VLine(img, x1, y1, y2, col)
	VLine(img, x2, y1, y2, col)
}

type drawRectOptions struct {
	X1, Y1, X2, Y2 int
	R, G, B        uint8
}

func processImage(input io.Reader, mainProc func(image.Image) image.Image) ([]byte, error) {
	m, format, err := image.Decode(input)
	if err != nil {
		return nil, err
	}

	m = mainProc(m)

	buf := new(bytes.Buffer)
	switch format {
	case "gif":
		gif.Encode(buf, m, nil)
	case "jpeg":
		quality := 100
		jpeg.Encode(buf, m, &jpeg.Options{Quality: quality})
	case "png":
		png.Encode(buf, m)
	default:
		return nil, fmt.Errorf("unknown format %s", format)
	}

	return buf.Bytes(), nil
}

func adjustBrightness(input io.Reader, percentage float64) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		return imaging.AdjustBrightness(m, percentage)
	})
}

func adjustContrast(input io.Reader, percentage float64) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		return imaging.AdjustContrast(m, percentage)
	})
}

func adjustGamma(input io.Reader, gamma float64) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		return imaging.AdjustGamma(m, gamma)
	})
}

func adjustSigmoid(input io.Reader, midpoint, factor float64) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		return imaging.AdjustSigmoid(m, midpoint, factor)
	})
}

func blur(input io.Reader, sigma float64) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		return imaging.Blur(m, sigma)
	})
}

func crop(input io.Reader, x1, y1, x2, y2 int) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		return imaging.Crop(m, image.Rect(x1, y1, x2, y2))
	})
}

func drawRect(input io.Reader, opts []*drawRectOptions) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		r := m.Bounds()
		m2 := image.NewRGBA(r)
		draw.Draw(m2, r, m, image.ZP, draw.Src)
		for _, opt := range opts {
			col := color.RGBA{opt.R, opt.G, opt.B, 255}
			RectLine(m2, opt.X1, opt.Y1, opt.X2, opt.Y2, col)
		}
		return m2
	})
}

func fit(input io.Reader, width, height int) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		return imaging.Fit(m, width, height, imaging.Lanczos)
	})
}

func flipH(input io.Reader) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		return imaging.FlipH(m)
	})
}

func flipV(input io.Reader) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		return imaging.FlipV(m)
	})
}

func grayscale(input io.Reader) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		return imaging.Grayscale(m)
	})
}

func invert(input io.Reader) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		return imaging.Invert(m)
	})
}

func sharpen(input io.Reader, sigma float64) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		return imaging.Sharpen(m, sigma)
	})
}

func transpose(input io.Reader) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		return imaging.Transpose(m)
	})
}

func transverse(input io.Reader) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		return imaging.Transverse(m)
	})
}

func resize(input io.Reader, w, h int) ([]byte, error) {
	return processImage(input, func(m image.Image) image.Image {
		return imaging.Resize(m, w, h, imaging.Lanczos)
	})
}

type ExpandArgs struct {
	Video string `json:"video"`
}

func (s *Server) Expand(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Path
	dir = dir[0 : len(dir)-len("_expand")]
	if !strings.HasSuffix(dir, "/") {
		http.Error(w, "expand should finish with '/'", http.StatusBadRequest)
		return
	}

	decoder := json.NewDecoder(r.Body)
	args := ExpandArgs{}
	if err := decoder.Decode(&args); err != nil {
		http.Error(w, "unrecognized args", http.StatusBadRequest)
		return
	}
	if args.Video == "" {
		http.Error(w, "\"video\" field is mandatory", http.StatusBadRequest)
		return
	}

	videopath := args.Video
	vUrl := extractTargetURL(videopath)
	if vUrl == "" {
		msg := fmt.Sprintf("target not found in path %s", videopath)
		http.Error(w, msg, http.StatusNotFound)
		return
	}

	resp, err := s.Client.Get(vUrl)
	if err != nil {
		glog.Error(err)
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if err := expand(s, resp.Body, dir, videopath); err != nil {
		glog.Error(err)
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
}

func makeInputHandlers(input io.Reader) *gmf.AVIOHandlers {
	reader, ok := input.(io.ReadSeeker)
	if !ok {
		// TODO: spill to disk if necessary
		glog.Info("Reader not seekable")
		buf := new(bytes.Buffer)
		io.Copy(buf, input)
		reader = bytes.NewReader(buf.Bytes())
	}

	return &gmf.AVIOHandlers{
		ReadPacket: func() ([]byte, int) {
			b := make([]byte, 512)
			n, err := reader.Read(b)
			if err != nil {
				glog.Error(err)
			}
			return b, n
		},
		WritePacket: func(b []byte) {
			glog.Error("unexpected Write call")
		},
		Seek: func(offset int64, whence int) int64 {
			n, err := reader.Seek(offset, whence)
			if whence != AVSEEK_SIZE && err != nil {
				glog.Error(err, fmt.Sprintf(" (offset = %d, whence = %d)", offset, whence))
			}
			return n
		},
	}
}

func expand(s *Server, input io.Reader, dir, objkey string) error {
	handlers := makeInputHandlers(input)

	ctx := gmf.NewCtx()
	ioctx, err := gmf.NewAVIOContext(ctx, handlers)
	ctx.SetPb(ioctx)
	defer ctx.CloseInputAndRelease()
	defer gmf.Release(ioctx)

	if err = ctx.OpenInput("dummy"); err != nil {
		glog.Error(err)
		return err
	}

	batch := new(leveldb.Batch)
	duration := float64(ctx.Duration())
	// format with padding so path key order agrees with our intension.
	npads := int(math.Log10(duration/1000000)) + 1
	snpads := strconv.Itoa(npads)
	for i := 0; i < int(duration/1000000)+1; i++ {
		// TODO: create relpath.  filepath.Rel() removes duplicate slashes, bad for us.
		//selfpath, err := filepath.Rel(dir, objkey)
		//if err != nil {
		//	glog.Error(err)
		//	break
		//}

		// Escape only the path part to distinguish it from query string.
		selfpath := selfURL(objkey)
		// query string can be raw.
		selfpath += fmt.Sprintf("?apply=frame&sec=%0"+snpads+"d", i)

		key := dir + selfpath
		meta := map[string]interface{}{}
		d := time.Duration(i) * time.Second
		meta["timestamp"] = fmt.Sprintf("%02d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
		meta["video"] = objkey
		value, _ := json.Marshal(&meta)
		_, _, err := s.PutObject([]byte(key), string(value), batch, true)
		if err != nil {
			return err
		}
	}

	if err := s.Db.Write(batch, nil); err != nil {
		glog.Error(err)
		return err
	}

	return nil
}

func frame(input io.Reader, sec int) ([]byte, error) {
	handlers := makeInputHandlers(input)

	ctx := gmf.NewCtx()
	defer ctx.CloseInputAndRelease()
	ioctx, err := gmf.NewAVIOContext(ctx, handlers)
	if err != nil {
		return nil, err
	}
	ctx.SetPb(ioctx)
	defer gmf.Release(ioctx)

	if err = ctx.OpenInput("dummy"); err != nil {
		glog.Error(err)
		return nil, err
	}

	srcVideoStream, err := ctx.GetBestStream(gmf.AVMEDIA_TYPE_VIDEO)
	if err != nil {
		glog.Error(err)
		return nil, err
	}

	if err = ctx.SeekFrameAt(sec, srcVideoStream.Index()); err != nil {
		glog.Error(err)
		return nil, err
	}

	codec, err := gmf.FindEncoder(gmf.AV_CODEC_ID_JPEG2000)
	if err != nil {
		glog.Error(err)
		return nil, err
	}

	cc := gmf.NewCodecCtx(codec)
	defer gmf.Release(cc)

	cc.SetPixFmt(gmf.AV_PIX_FMT_RGB24).
		SetWidth(srcVideoStream.CodecCtx().Width()).
		SetHeight(srcVideoStream.CodecCtx().Height())

	if codec.IsExperimental() {
		cc.SetStrictCompliance(gmf.FF_COMPLIANCE_EXPERIMENTAL)
	}

	if err = cc.Open(nil); err != nil {
		glog.Error(err)
		return nil, err
	}
	defer cc.Close()

	// Just to surprress "deprected format" warning...
	cc.SetPixFmt(gmf.AV_PIX_FMT_RGB24)

	// This is necessary to avoid leaking thread used by codec.
	defer srcVideoStream.CodecCtx().Close()

	swsCtx := gmf.NewSwsCtx(srcVideoStream.CodecCtx(), cc, gmf.SWS_POINT)
	defer gmf.Release(swsCtx)

	dstFrame := gmf.NewFrame().
		SetWidth(srcVideoStream.CodecCtx().Width()).
		SetHeight(srcVideoStream.CodecCtx().Height()).
		SetFormat(gmf.AV_PIX_FMT_RGB24)
	defer gmf.Release(dstFrame)

	if err := dstFrame.ImgAlloc(); err != nil {
		glog.Error(err)
		return nil, err
	}

	for {
		packet := ctx.GetNextPacket()
		if packet == nil {
			break
		}

		// Wrap by anonymous func so we can use defer for each iteration.
		data, err := func(packet *gmf.Packet) ([]byte, error) {
			defer gmf.Release(packet)

			if packet.StreamIndex() != srcVideoStream.Index() {
				return nil, nil
			}
			ist, err := ctx.GetStream(packet.StreamIndex())
			if err != nil {
				return nil, err
			}

			ready := false
			var buf *bytes.Buffer
			for !ready {
				frame, err := packet.GetNextFrame(ist.CodecCtx())
				if frame == nil || err != nil {
					return nil, err
				}

				if glog.V(5) {
					glog.Info(fmt.Sprintf("desired = %v, actual = %v", sec, frame.TimeStamp()))
				}
				swsCtx.Scale(frame, dstFrame)

				ready = sec*1000 <= frame.TimeStamp()

				if ready {
					// Encode RGB24 to RGBA to JPEG.
					// TODO: we could avoid even copy with the loop
					// by introducing RGB type implementing image.Image
					streamIndex := 0 // not sure how to determine this??
					src := dstFrame.Data(streamIndex)
					img := image.NewRGBA(image.Rect(0, 0, dstFrame.Width(), dstFrame.Height()))
					stride := img.Stride
					linesize := dstFrame.LineSize(streamIndex)
					for y := 0; y < dstFrame.Height(); y++ {
						for x := 0; x < dstFrame.Width(); x++ {
							img.Pix[y*stride+x*4+0] = src[y*linesize+x*3+0]
							img.Pix[y*stride+x*4+1] = src[y*linesize+x*3+1]
							img.Pix[y*stride+x*4+2] = src[y*linesize+x*3+2]
							img.Pix[y*stride+x*4+3] = 0
						}
					}
					buf = new(bytes.Buffer)
					jpeg.Encode(buf, img, &jpeg.Options{Quality: 100})
				}

				gmf.Release(frame)

				if ready {
					return buf.Bytes(), nil
				}
			}

			return nil, nil
		}(packet)

		// Error?
		if err != nil {
			return nil, err
		}
		// Done?
		if data != nil {
			return data, nil
		}
	}

	// Did we not find frame?
	return nil, fmt.Errorf("unexpected end of stream")
}

// --- snippet
// curl localhost:8592/path/mp4/slice/ | jq -r '. | sort_by(.metadata.timestamp) | .[] | "\(.metadata.timestamp)<img src=\"http://localhost:8592\(._filepath)\"><br/>"' | sed -e 's/%/%25/g' | sed -e 's/?/%3F/'
