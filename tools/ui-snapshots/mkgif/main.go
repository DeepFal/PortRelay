// mkgif 把逐帧 PNG 合成为循环播放的 GIF，用于 README 动图。
//
//	go run ./tools/ui-snapshots/mkgif -o docs/images/live-traffic.gif tools/ui-snapshots/out/gif-*.png
//
// 所有帧共用一套中位切分调色板；第二帧起只编码与上一帧不同的区域，
// 区域内未变化的像素写成透明色，界面类动图体积因此很小。
package main

import (
	"flag"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"log"
	"os"
	"sort"
)

func main() {
	out := flag.String("o", "out.gif", "output file")
	delay := flag.Int("delay", 40, "frame delay in 1/100 s")
	hold := flag.Int("hold", 120, "extra delay on the last frame in 1/100 s")
	shrink := flag.Int("shrink", 1, "downscale factor (box filter)")
	flag.Parse()
	if flag.NArg() == 0 {
		log.Fatal("no input frames")
	}

	var frames []*image.RGBA
	for _, path := range flag.Args() {
		frames = append(frames, downscale(load(path), *shrink))
	}
	bounds := frames[0].Bounds()

	pal := medianCut(histogram(frames), 255)
	transparent := uint8(len(pal))
	pal = append(pal, color.RGBA{})

	idx := newMapper(pal[:transparent])
	anim := &gif.GIF{LoopCount: 0, Config: image.Config{ColorModel: pal, Width: bounds.Dx(), Height: bounds.Dy()}}
	var prev *image.Paletted
	for i, f := range frames {
		cur := image.NewPaletted(bounds, pal)
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				cur.SetColorIndex(x, y, idx.index(f.RGBAAt(x, y)))
			}
		}
		frame := cur
		if prev != nil {
			frame = diff(prev, cur, transparent)
		}
		d := *delay
		if i == len(frames)-1 {
			d += *hold
		}
		anim.Image = append(anim.Image, frame)
		anim.Delay = append(anim.Delay, d)
		anim.Disposal = append(anim.Disposal, gif.DisposalNone)
		prev = cur
	}

	w, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	if err := gif.EncodeAll(w, anim); err != nil {
		log.Fatal(err)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}
	st, _ := os.Stat(*out)
	log.Printf("%s: %d frames, %dx%d, %d KB", *out, len(frames), bounds.Dx(), bounds.Dy(), st.Size()/1024)
}

func load(path string) *image.RGBA {
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		log.Fatalf("%s: %v", path, err)
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			rgba.Set(x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return rgba
}

func downscale(src *image.RGBA, k int) *image.RGBA {
	if k <= 1 {
		return src
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx()/k, b.Dy()/k))
	for y := 0; y < dst.Bounds().Dy(); y++ {
		for x := 0; x < dst.Bounds().Dx(); x++ {
			var r, g, bl, a int
			for dy := 0; dy < k; dy++ {
				for dx := 0; dx < k; dx++ {
					c := src.RGBAAt(x*k+dx, y*k+dy)
					r, g, bl, a = r+int(c.R), g+int(c.G), bl+int(c.B), a+int(c.A)
				}
			}
			n := k * k
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n), uint8(g / n), uint8(bl / n), uint8(a / n)})
		}
	}
	return dst
}

type bucket struct {
	c     [3]uint8
	count int
}

func histogram(frames []*image.RGBA) []bucket {
	counts := map[[3]uint8]int{}
	for _, f := range frames {
		for i := 0; i+3 < len(f.Pix); i += 4 {
			counts[[3]uint8{f.Pix[i], f.Pix[i+1], f.Pix[i+2]}]++
		}
	}
	out := make([]bucket, 0, len(counts))
	for c, n := range counts {
		out = append(out, bucket{c, n})
	}
	return out
}

// medianCut 反复把"像素数 × 色彩跨度"最大的盒子沿最长轴按像素中位数切开。
func medianCut(colors []bucket, n int) color.Palette {
	boxes := [][]bucket{colors}
	for len(boxes) < n {
		best, bestScore, bestAxis := -1, 0, 0
		for i, b := range boxes {
			if len(b) < 2 {
				continue
			}
			axis, span := longestAxis(b)
			total := 0
			for _, c := range b {
				total += c.count
			}
			if score := span * total; score > bestScore {
				best, bestScore, bestAxis = i, score, axis
			}
		}
		if best < 0 {
			break
		}
		b := boxes[best]
		sort.Slice(b, func(i, j int) bool { return b[i].c[bestAxis] < b[j].c[bestAxis] })
		total := 0
		for _, c := range b {
			total += c.count
		}
		cut, acc := 1, 0
		for i, c := range b {
			acc += c.count
			if acc*2 >= total {
				cut = i + 1
				break
			}
		}
		if cut >= len(b) {
			cut = len(b) - 1
		}
		boxes[best] = b[:cut]
		boxes = append(boxes, b[cut:])
	}
	pal := make(color.Palette, 0, len(boxes))
	for _, b := range boxes {
		var r, g, bl, total int
		for _, c := range b {
			r += int(c.c[0]) * c.count
			g += int(c.c[1]) * c.count
			bl += int(c.c[2]) * c.count
			total += c.count
		}
		pal = append(pal, color.RGBA{uint8(r / total), uint8(g / total), uint8(bl / total), 255})
	}
	return pal
}

func longestAxis(b []bucket) (axis, span int) {
	for a := 0; a < 3; a++ {
		lo, hi := 255, 0
		for _, c := range b {
			v := int(c.c[a])
			lo, hi = min(lo, v), max(hi, v)
		}
		if hi-lo > span {
			axis, span = a, hi-lo
		}
	}
	return axis, span
}

type mapper struct {
	pal   color.Palette
	cache map[color.RGBA]uint8
}

func newMapper(pal color.Palette) *mapper {
	return &mapper{pal: pal, cache: map[color.RGBA]uint8{}}
}

func (m *mapper) index(c color.RGBA) uint8 {
	c.A = 255
	if i, ok := m.cache[c]; ok {
		return i
	}
	best, bestD := 0, int(^uint(0)>>1)
	for i, p := range m.pal {
		q := p.(color.RGBA)
		dr, dg, db := int(c.R)-int(q.R), int(c.G)-int(q.G), int(c.B)-int(q.B)
		if d := 3*dr*dr + 4*dg*dg + 2*db*db; d < bestD {
			best, bestD = i, d
		}
	}
	m.cache[c] = uint8(best)
	return uint8(best)
}

// diff 返回只覆盖变化区域的帧，区域内未变化的像素为透明色。
func diff(prev, cur *image.Paletted, transparent uint8) *image.Paletted {
	b := cur.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X-1, b.Min.Y-1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if prev.ColorIndexAt(x, y) != cur.ColorIndexAt(x, y) {
				minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
			}
		}
	}
	if maxX < minX {
		minX, minY, maxX, maxY = 0, 0, 0, 0
	}
	r := image.Rect(minX, minY, maxX+1, maxY+1)
	out := image.NewPaletted(r, cur.Palette)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if v := cur.ColorIndexAt(x, y); v != prev.ColorIndexAt(x, y) {
				out.SetColorIndex(x, y, v)
			} else {
				out.SetColorIndex(x, y, transparent)
			}
		}
	}
	return out
}
