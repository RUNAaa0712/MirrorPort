package main

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
)

const mosaicScale = 10000

type mosaicJob struct {
	frame   []byte
	region  mosaicRegion
	version uint64
}

func (s *receiverSession) startMosaicWorker(r *receiver) {
	s.mosaicJobs = make(chan mosaicJob, 1)
	go func() {
		for {
			select {
			case <-s.ctx.Done():
				return
			case job := <-s.mosaicJobs:
				frame, err := mosaicFrame(job.frame, job.region, s.jpegQuality)
				r.mu.Lock()
				if !s.stopping && s.ctx.Err() == nil && job.version == s.mosaicVersion {
					if err != nil {
						frame = nil
					}
					s.replaceFrame(frame)
				}
				r.mu.Unlock()
			}
		}
	}()
}

// Coordinates are scaled to 0..10000 relative to the source frame.
type mosaicRegion struct {
	Left   int `json:"left"`
	Top    int `json:"top"`
	Right  int `json:"right"`
	Bottom int `json:"bottom"`
}

func (m mosaicRegion) valid() bool {
	return m.Left >= 0 && m.Top >= 0 && m.Right <= mosaicScale && m.Bottom <= mosaicScale &&
		m.Right > m.Left && m.Bottom > m.Top
}

func (r *receiver) updateMosaic(id string, region *mosaicRegion) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.current
	if s == nil || s.stopping || s.status.ID != id || s.status.Display != "browser" {
		return errors.New("範囲を設定できる受信機が見つかりません。")
	}
	s.status.Mosaic = region
	s.mosaicVersion++
	s.replaceFrame(nil)
	return nil
}

func mosaicFrame(frame []byte, region mosaicRegion, quality int) ([]byte, error) {
	decoded, err := jpeg.Decode(bytes.NewReader(frame))
	if err != nil {
		return nil, err
	}
	bounds := decoded.Bounds()
	area := image.Rect(
		bounds.Min.X+bounds.Dx()*region.Left/mosaicScale,
		bounds.Min.Y+bounds.Dy()*region.Top/mosaicScale,
		bounds.Min.X+(bounds.Dx()*region.Right+mosaicScale-1)/mosaicScale,
		bounds.Min.Y+(bounds.Dy()*region.Bottom+mosaicScale-1)/mosaicScale,
	)
	size := max(12, min(area.Dx(), area.Dy())/8, max(area.Dx(), area.Dy())/24)
	switch img := decoded.(type) {
	case *image.YCbCr:
		for y := area.Min.Y; y < area.Max.Y; y += size {
			for x := area.Min.X; x < area.Max.X; x += size {
				right, bottom := min(x+size, area.Max.X), min(y+size, area.Max.Y)
				sample := img.YCbCrAt((x+right)/2, (y+bottom)/2)
				for row := y; row < bottom; row++ {
					for col := x; col < right; col++ {
						img.Y[img.YOffset(col, row)] = sample.Y
						chroma := img.COffset(col, row)
						img.Cb[chroma], img.Cr[chroma] = sample.Cb, sample.Cr
					}
				}
			}
		}
	case *image.Gray:
		for y := area.Min.Y; y < area.Max.Y; y += size {
			for x := area.Min.X; x < area.Max.X; x += size {
				right, bottom := min(x+size, area.Max.X), min(y+size, area.Max.Y)
				sample := img.GrayAt((x+right)/2, (y+bottom)/2)
				for row := y; row < bottom; row++ {
					for col := x; col < right; col++ {
						img.SetGray(col, row, sample)
					}
				}
			}
		}
	default:
		return nil, errors.New("unsupported JPEG format")
	}
	var output bytes.Buffer
	err = jpeg.Encode(&output, decoded, &jpeg.Options{Quality: 100 - quality*4})
	return output.Bytes(), err
}
