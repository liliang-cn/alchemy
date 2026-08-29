package document

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"

	pdflib "github.com/ledongthuc/pdf"
)

// Getting a page image out of a PDF so an OCR model can read it.
//
// alchemy.OCR is handed "the raw bytes of one page image", so this package has
// to produce one. It does not rasterise the page — that would need a font
// engine and a rendering library, and DESIGN.md's stdlib-plus-one budget does
// not stretch to either. What it does is take the image the scanner already
// put there: a scanned page is one full-page image XObject, and that XObject
// is the page.
//
// A page whose image cannot be produced is reported unread with the reason.
// The one thing that never happens is handing OCR bytes that are not an image.

// pageImage is what gets handed to the OCR model.
type pageImage struct {
	data      []byte
	mediaType string
}

// extractPageImage returns the largest image drawn on the page.
func extractPageImage(doc *pdflib.Reader, num int, jpegs *jpegFinder) (img pageImage, err error) {
	defer func() {
		if r := recover(); r != nil {
			img, err = pageImage{}, fmt.Errorf("%v", r)
		}
	}()

	best, width, height := pdflib.Value{}, 0, 0
	xobj := doc.Page(num).Resources().Key("XObject")
	for _, name := range xobj.Keys() {
		v := xobj.Key(name)
		if v.Key("Subtype").Name() != "Image" {
			continue
		}
		w, h := int(v.Key("Width").Int64()), int(v.Key("Height").Int64())
		if w*h > width*height {
			best, width, height = v, w, h
		}
	}
	if width == 0 || height == 0 {
		return pageImage{}, fmt.Errorf("the page draws no image")
	}

	switch filter := streamFilter(best); filter {
	case "DCTDecode":
		// The stream is a JPEG file already. The parser will not hand over an
		// undecoded stream, so the bytes are recovered from the spooled file
		// and then verified to be a JPEG of exactly the declared size — an
		// unverified guess is not something to send to a model.
		data, ok := jpegs.find(width, height, int(best.Key("Length").Int64()))
		if !ok {
			return pageImage{}, fmt.Errorf("the page's JPEG image could not be recovered from the file")
		}
		return pageImage{data: data, mediaType: "image/jpeg"}, nil
	case "", "FlateDecode", "ASCII85Decode", "LZWDecode":
		return encodeSamples(best, width, height)
	default:
		return pageImage{}, fmt.Errorf("the page image uses the %s filter, which this reader cannot decode", filter)
	}
}

// streamFilter names the stream's filter, or "" when it has none. A filter
// chain of more than one is reported joined, so the unread reason still says
// something a person can look up.
func streamFilter(v pdflib.Value) string {
	f := v.Key("Filter")
	switch f.Kind() {
	case pdflib.Name:
		return f.Name()
	case pdflib.Array:
		names := make([]byte, 0, 32)
		for i := 0; i < f.Len(); i++ {
			if i > 0 {
				names = append(names, '+')
			}
			names = append(names, f.Index(i).Name()...)
		}
		return string(names)
	default:
		return ""
	}
}

// encodeSamples re-encodes a decoded image XObject as PNG. Only the two colour
// spaces a scanner actually writes at 8 bits are handled; anything else is
// reported rather than approximated, because an approximated page image is a
// page a model would read wrongly and nobody would know.
func encodeSamples(v pdflib.Value, width, height int) (pageImage, error) {
	if bpc := v.Key("BitsPerComponent").Int64(); bpc != 8 {
		return pageImage{}, fmt.Errorf("the page image is %d bits per component, which this reader cannot decode", bpc)
	}
	space := v.Key("ColorSpace").Name()
	var comps int
	switch space {
	case "DeviceGray", "CalGray", "G":
		comps = 1
	case "DeviceRGB", "CalRGB", "RGB":
		comps = 3
	default:
		return pageImage{}, fmt.Errorf("the page image uses the %s colour space, which this reader cannot decode", orUnknown(space))
	}

	want := width * height * comps
	rd := v.Reader()
	defer rd.Close()
	// One page image, bounded by its own declared size: a corrupt /Length
	// cannot make this allocate the document.
	samples, err := io.ReadAll(io.LimitReader(rd, int64(want)))
	if err != nil {
		return pageImage{}, fmt.Errorf("the page image could not be decompressed: %w", err)
	}
	if len(samples) < want {
		return pageImage{}, fmt.Errorf("the page image is truncated: %d of %d samples", len(samples), want)
	}

	var img image.Image
	if comps == 1 {
		g := image.NewGray(image.Rect(0, 0, width, height))
		copy(g.Pix, samples)
		img = g
	} else {
		c := image.NewRGBA(image.Rect(0, 0, width, height))
		for i, j := 0, 0; i+2 < len(samples); i, j = i+3, j+4 {
			c.Pix[j], c.Pix[j+1], c.Pix[j+2], c.Pix[j+3] = samples[i], samples[i+1], samples[i+2], 0xff
		}
		img = c
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return pageImage{}, fmt.Errorf("the page image could not be encoded: %w", err)
	}
	return pageImage{data: out.Bytes(), mediaType: "image/png"}, nil
}

func orUnknown(s string) string {
	if s == "" {
		return "unnamed"
	}
	return s
}

// jpegFinder locates JPEG streams in the spooled file.
//
// The PDF parser decodes streams it understands and panics on the ones it does
// not, so there is no way to ask it for the undecoded bytes of a DCTDecode
// image. Those bytes are a JPEG file, and a JPEG stream always begins right
// after the "stream" keyword — so one pass over the file finds every candidate,
// and a candidate is only used when it decodes to the size the XObject
// declared. The pass is done once per document and cached.
type jpegFinder struct {
	f       io.ReaderAt
	size    int64
	offsets []int64
	scanned bool
}

var streamSOI = []byte("stream")

func (j *jpegFinder) scan() {
	j.scanned = true
	const window = 64 << 10
	// The longest match is "stream" + CRLF + SOI, so overlapping reads by that
	// much means no candidate can fall across a chunk boundary.
	const overlap = 16
	buf := make([]byte, window+overlap)
	for base := int64(0); base < j.size; base += window {
		n, err := j.f.ReadAt(buf, base)
		if n == 0 && err != nil {
			return
		}
		chunk := buf[:n]
		for i := 0; ; {
			k := bytes.Index(chunk[i:], streamSOI)
			if k < 0 {
				break
			}
			at := i + k + len(streamSOI)
			i = i + k + 1
			p := at
			if p < len(chunk) && chunk[p] == '\r' {
				p++
			}
			if p < len(chunk) && chunk[p] == '\n' {
				p++
			}
			if p+3 <= len(chunk) && chunk[p] == 0xff && chunk[p+1] == 0xd8 && chunk[p+2] == 0xff {
				j.offsets = append(j.offsets, base+int64(p))
			}
		}
	}
}

// find returns the JPEG of the given declared length whose header says it is
// width by height. Verification is the point: without it this would be a guess.
func (j *jpegFinder) find(width, height, length int) ([]byte, bool) {
	if !j.scanned {
		j.scan()
	}
	if length <= 0 {
		return nil, false
	}
	for _, off := range j.offsets {
		if off+int64(length) > j.size {
			continue
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(io.NewSectionReader(j.f, off, int64(length)), data); err != nil {
			continue
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width != width || cfg.Height != height {
			continue
		}
		return data, true
	}
	return nil, false
}
