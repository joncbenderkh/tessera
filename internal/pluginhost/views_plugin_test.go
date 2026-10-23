// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// The test plugin's view calls; server lists the modes.

func textView(s string) *tesserav1.View {
	return &tesserav1.View{Body: textWidget(s)}
}

func textWidget(s string) *tesserav1.Widget {
	return &tesserav1.Widget{Kind: &tesserav1.Widget_Text{Text: &tesserav1.Text{Value: s}}}
}

// nested is a view whose widgets nest depth deep.
func nested(depth int) *tesserav1.View {
	w := textWidget("bottom")
	for range depth - 1 {
		w = &tesserav1.Widget{Kind: &tesserav1.Widget_Stack{Stack: &tesserav1.Stack{
			Items: []*tesserav1.StackItem{{Widget: w}},
		}}}
	}
	return &tesserav1.View{Body: w}
}

func polledMode(mode string) bool {
	switch mode {
	case "polled", "polled-failing", "polled-quota", "polled-unavailable", "polled-empty", "polled-deep", "polled-big", "polled-event-deep", "polled-events", "hangs-render", "no-views":
		return true
	}
	return false
}

func (s *server) Watch(req *tesserav1.WatchRequest, stream tesserav1.PanelService_WatchServer) error {
	ctx := stream.Context()
	send := func(v *tesserav1.View) error {
		return stream.Send(&tesserav1.WatchResponse{Content: &tesserav1.WatchResponse_View{View: v}})
	}
	switch {
	case polledMode(s.mode):
		return s.UnimplementedPanelServiceServer.Watch(req, stream)
	case s.mode == "silent-watch":
		send(textView("once"))
		<-ctx.Done()
		return nil
	case s.mode == "ends-watch":
		send(textView("once"))
		return nil
	case s.mode == "late-unimplemented":
		send(textView("once"))
		return status.Error(codes.Unimplemented, "changed its mind")
	case s.mode == "quota-watch":
		send(textView("once"))
		return status.Error(codes.ResourceExhausted, "quota exceeded")
	case s.mode == "deep-view":
		if err := send(nested(17)); err != nil {
			return err
		}
	case s.mode == "deep-then-good":
		send(nested(17))
		if err := send(textView("good")); err != nil {
			return err
		}
	case s.mode == "too-deep-to-decode":
		if err := send(nested(4000)); err != nil {
			return err
		}
	case s.mode == "big-view":
		if err := send(textView(strings.Repeat("x", 5<<20))); err != nil {
			return err
		}
	case s.mode == "huge-view":
		if err := send(textView(strings.Repeat("x", 17<<20))); err != nil {
			return err
		}
	case s.mode == "spams-views":
		for i := 0; ; i++ {
			if err := send(textView(fmt.Sprint("view ", i))); err != nil {
				return err
			}
		}
	default:
		if err := send(textView(s.mode)); err != nil {
			return err
		}
	}
	beat := time.NewTicker(req.GetHeartbeatInterval().AsDuration())
	defer beat.Stop()
	for {
		var err error
		select {
		case <-ctx.Done():
			return nil
		case <-beat.C:
			err = stream.Send(&tesserav1.WatchResponse{})
		case id := <-s.events:
			err = send(textView("got " + id))
		}
		if err != nil {
			return err
		}
	}
}

func (s *server) Render(ctx context.Context, _ *tesserav1.RenderRequest) (*tesserav1.RenderResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var text string
	switch s.mode {
	case "no-views":
		return s.UnimplementedPanelServiceServer.Render(ctx, nil)
	case "hangs-render":
		s.mu.Unlock()
		<-ctx.Done()
		s.mu.Lock()
		return nil, ctx.Err()
	case "polled-failing":
		if _, err := os.Stat(os.Args[0] + ".open"); s.renders > 0 && err != nil {
			return nil, status.Error(codes.Internal, "source down")
		}
		text = fmt.Sprint("render ", s.renders)
	case "polled-quota", "polled-unavailable", "polled-empty":
		s.renders++
		switch {
		case s.renders == 2 && s.mode == "polled-quota": // passed on from a client of its own
			return nil, status.Error(codes.ResourceExhausted, "grpc: received message larger than max (5000000 vs. 4194304)")
		case s.renders == 2 && s.mode == "polled-unavailable":
			return nil, status.Error(codes.Unavailable, "source down")
		case s.renders == 2:
			return nil, status.Error(codes.Internal, "blip")
		case s.renders > 2 && s.mode == "polled-empty":
			return &tesserav1.RenderResponse{PollInterval: durationpb.New(100 * time.Millisecond)}, nil
		}
		return &tesserav1.RenderResponse{
			Content:      &tesserav1.RenderResponse_View{View: textView(fmt.Sprint("render ", s.renders-1))},
			PollInterval: durationpb.New(100 * time.Millisecond),
		}, nil
	case "polled-deep":
		return &tesserav1.RenderResponse{Content: &tesserav1.RenderResponse_View{View: nested(17)}}, nil
	case "polled-event-deep":
		s.renders++
		resp := &tesserav1.RenderResponse{
			Content:      &tesserav1.RenderResponse_View{View: textView(fmt.Sprint("render ", s.renders-1))},
			PollInterval: durationpb.New(250 * time.Millisecond),
		}
		if s.deep {
			s.deep = false
			resp.Content = &tesserav1.RenderResponse_View{View: nested(17)}
		}
		return resp, nil
	case "polled-big":
		return &tesserav1.RenderResponse{Content: &tesserav1.RenderResponse_View{View: textView(strings.Repeat("x", 5<<20))}}, nil
	case "polled-events":
		s.seen = append(s.seen, "render")
		text = strings.Join(s.seen, ",")
	default:
		text = fmt.Sprint("render ", s.renders)
	}
	s.renders++
	resp := &tesserav1.RenderResponse{Content: &tesserav1.RenderResponse_View{View: textView(text)}}
	if s.mode != "polled-events" { // which the host renders only for its events
		resp.PollInterval = durationpb.New(100 * time.Millisecond)
	}
	return resp, nil
}

func (s *server) HandleEvent(ctx context.Context, req *tesserav1.HandleEventRequest) (*tesserav1.HandleEventResponse, error) {
	id := req.GetActivate().GetActionId()
	switch s.mode {
	case "hangs-event":
		<-ctx.Done()
		return nil, ctx.Err()
	case "unavailable-event":
		return nil, status.Error(codes.Unavailable, "source down")
	case "slow-events":
		time.Sleep(150 * time.Millisecond)
		if sc := req.GetScroll(); sc != nil {
			fmt.Fprintf(os.Stderr, "scroll %s %d\n", sc.GetWidgetId(), sc.GetFirstVisible())
		} else {
			fmt.Fprintf(os.Stderr, "activate %s\n", id)
		}
	case "polled-events":
		s.mu.Lock()
		s.seen = append(s.seen, id)
		s.mu.Unlock()
	case "polled-event-deep":
		s.mu.Lock()
		s.deep = true
		s.mu.Unlock()
	default:
		s.events <- id
	}
	return &tesserav1.HandleEventResponse{}, nil
}
