package computer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Desktop-Steuerung für den virtuellen Bildschirm der Fylgja (X11 über xdotool/ImageMagick).
// Die Aktionen laufen mit dem Token von computerd; welche Aktion wann erlaubt ist, entscheidet
// die Policy im Control Plane. Hier wird nur validiert, damit Argumente nie als Optionen oder
// Shell-Kommandos interpretiert werden.

// Runner führt ein Programm aus und liefert stdout.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func defaultRunner(display string) Runner {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = append(os.Environ(), "DISPLAY="+display)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(errb.String()))
		}
		return out.Bytes(), nil
	}
}

var keyToken = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_+\-]{0,39}$`)

const (
	maxTypeChars  = 4000
	maxShotBytes  = 400 << 10
	maxWaitMillis = 10_000
)

type desktopReq struct {
	X, Y   *int   `json:"x,omitempty"`
	ToX    *int   `json:"to_x,omitempty"`
	ToY    *int   `json:"to_y,omitempty"`
	Text   string `json:"text,omitempty"`
	Keys   string `json:"keys,omitempty"`
	DX, DY int    `json:"dx,omitempty"`
	Ms     int    `json:"ms,omitempty"`
}

type desktopResp struct {
	Text   string `json:"text"`
	Image  string `json:"image,omitempty"` // data:image/jpeg;base64,…
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

func (s *Server) runner() Runner {
	if s.Run != nil {
		return s.Run
	}
	d := s.Display
	if d == "" {
		d = ":1"
	}
	return defaultRunner(d)
}

func (s *Server) geometry(ctx context.Context) (int, int, error) {
	out, err := s.runner()(ctx, "xdotool", "getdisplaygeometry")
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		return 0, 0, errors.New("bildschirmgröße nicht lesbar")
	}
	w, e1 := strconv.Atoi(f[0])
	h, e2 := strconv.Atoi(f[1])
	if e1 != nil || e2 != nil || w <= 0 || h <= 0 {
		return 0, 0, errors.New("bildschirmgröße ungültig")
	}
	return w, h, nil
}

func inBounds(p *int, max int, name string) (int, error) {
	if p == nil {
		return 0, fmt.Errorf("%s fehlt", name)
	}
	if *p < 0 || *p >= max {
		return 0, fmt.Errorf("%s=%d liegt außerhalb des Bildschirms (0..%d)", name, *p, max-1)
	}
	return *p, nil
}

func (s *Server) screenshot(ctx context.Context) (desktopResp, error) {
	w, h, err := s.geometry(ctx)
	if err != nil {
		return desktopResp{}, err
	}
	img, err := s.runner()(ctx, "import", "-window", "root", "-quality", "62", "jpeg:-")
	if err != nil {
		return desktopResp{}, err
	}
	if len(img) > maxShotBytes {
		img, err = s.runner()(ctx, "import", "-window", "root", "-quality", "35", "jpeg:-")
		if err != nil || len(img) > maxShotBytes {
			return desktopResp{}, errors.New("screenshot zu groß")
		}
	}
	return desktopResp{Text: fmt.Sprintf("Bildschirm %dx%d. Koordinaten beziehen sich auf dieses Bild.", w, h),
		Image: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(img), Width: w, Height: h}, nil
}

func (s *Server) desktopAction(ctx context.Context, action string, a desktopReq) (desktopResp, error) {
	run := s.runner()
	switch action {
	case "screenshot":
		return s.screenshot(ctx)
	case "info":
		w, h, err := s.geometry(ctx)
		return desktopResp{Text: fmt.Sprintf("%dx%d", w, h), Width: w, Height: h}, err
	case "wait":
		ms := min(max(a.Ms, 0), maxWaitMillis)
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
		case <-ctx.Done():
			return desktopResp{}, ctx.Err()
		}
		return desktopResp{Text: fmt.Sprintf("%d ms gewartet", ms)}, nil
	case "move", "click", "double_click", "right_click", "middle_click":
		w, h, err := s.geometry(ctx)
		if err != nil {
			return desktopResp{}, err
		}
		x, err := inBounds(a.X, w, "x")
		if err != nil {
			return desktopResp{}, err
		}
		y, err := inBounds(a.Y, h, "y")
		if err != nil {
			return desktopResp{}, err
		}
		args := []string{"mousemove", "--sync", strconv.Itoa(x), strconv.Itoa(y)}
		switch action {
		case "click":
			args = append(args, "click", "1")
		case "double_click":
			args = append(args, "click", "--repeat", "2", "--delay", "80", "1")
		case "right_click":
			args = append(args, "click", "3")
		case "middle_click":
			args = append(args, "click", "2")
		}
		if _, err := run(ctx, "xdotool", args...); err != nil {
			return desktopResp{}, err
		}
		return desktopResp{Text: fmt.Sprintf("%s bei (%d,%d)", action, x, y)}, nil
	case "drag":
		w, h, err := s.geometry(ctx)
		if err != nil {
			return desktopResp{}, err
		}
		x, e1 := inBounds(a.X, w, "x")
		y, e2 := inBounds(a.Y, h, "y")
		tx, e3 := inBounds(a.ToX, w, "to_x")
		ty, e4 := inBounds(a.ToY, h, "to_y")
		if err := errors.Join(e1, e2, e3, e4); err != nil {
			return desktopResp{}, err
		}
		args := []string{"mousemove", "--sync", strconv.Itoa(x), strconv.Itoa(y), "mousedown", "1", "mousemove", "--sync", strconv.Itoa(tx), strconv.Itoa(ty), "mouseup", "1"}
		if _, err := run(ctx, "xdotool", args...); err != nil {
			return desktopResp{}, err
		}
		return desktopResp{Text: fmt.Sprintf("gezogen von (%d,%d) nach (%d,%d)", x, y, tx, ty)}, nil
	case "type":
		if a.Text == "" || len([]rune(a.Text)) > maxTypeChars {
			return desktopResp{}, fmt.Errorf("text fehlt oder ist länger als %d Zeichen", maxTypeChars)
		}
		// "--" verhindert, dass Text mit führendem "-" als Option gelesen wird.
		if _, err := run(ctx, "xdotool", "type", "--delay", "12", "--clearmodifiers", "--", a.Text); err != nil {
			return desktopResp{}, err
		}
		return desktopResp{Text: fmt.Sprintf("%d Zeichen getippt", len([]rune(a.Text)))}, nil
	case "key":
		toks := strings.Fields(a.Keys)
		if len(toks) == 0 || len(toks) > 8 {
			return desktopResp{}, errors.New("keys: 1 bis 8 Tasten(-kombinationen), z. B. \"ctrl+l\" oder \"Return\"")
		}
		for _, t := range toks {
			if !keyToken.MatchString(t) {
				return desktopResp{}, fmt.Errorf("ungültige taste %q", t)
			}
		}
		if _, err := run(ctx, "xdotool", append([]string{"key", "--clearmodifiers", "--"}, toks...)...); err != nil {
			return desktopResp{}, err
		}
		return desktopResp{Text: "gedrückt: " + strings.Join(toks, " ")}, nil
	case "scroll":
		w, h, err := s.geometry(ctx)
		if err != nil {
			return desktopResp{}, err
		}
		if a.X != nil && a.Y != nil {
			x, e1 := inBounds(a.X, w, "x")
			y, e2 := inBounds(a.Y, h, "y")
			if err := errors.Join(e1, e2); err != nil {
				return desktopResp{}, err
			}
			if _, err := run(ctx, "xdotool", "mousemove", "--sync", strconv.Itoa(x), strconv.Itoa(y)); err != nil {
				return desktopResp{}, err
			}
		}
		btn, n := "5", a.DY
		switch {
		case a.DY < 0:
			btn, n = "4", -a.DY
		case a.DY == 0 && a.DX > 0:
			btn, n = "7", a.DX
		case a.DY == 0 && a.DX < 0:
			btn, n = "6", -a.DX
		}
		n = min(max(n, 1), 30)
		if _, err := run(ctx, "xdotool", "click", "--repeat", strconv.Itoa(n), "--delay", "30", btn); err != nil {
			return desktopResp{}, err
		}
		return desktopResp{Text: fmt.Sprintf("%d Rasterschritte gescrollt", n)}, nil
	}
	return desktopResp{}, fmt.Errorf("unbekannte desktop-aktion %q", action)
}

func (s *Server) desktop(w http.ResponseWriter, r *http.Request) {
	var a desktopReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&a); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "ungültiger body", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := s.desktopAction(ctx, r.PathValue("action"), a)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, res)
}
