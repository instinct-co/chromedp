package chromedp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
)

func TestDOMEventsDuringDocumentRefresh(t *testing.T) {
	cases := []struct {
		name  string
		event func(cdp.NodeID) any
	}{
		{"attribute", func(_ cdp.NodeID) any { return &dom.EventAttributeModified{NodeID: 1, Name: "class", Value: "ready"} }},
		{"set children", func(id cdp.NodeID) any { return &dom.EventSetChildNodes{ParentID: 1, Nodes: []*cdp.Node{{NodeID: id}}} }},
		{"insert child", func(id cdp.NodeID) any {
			return &dom.EventChildNodeInserted{ParentNodeID: 1, Node: &cdp.Node{NodeID: id}}
		}},
		{"remove child", func(_ cdp.NodeID) any { return &dom.EventChildNodeRemoved{ParentNodeID: 1, NodeID: 2} }},
		{"push shadow", func(id cdp.NodeID) any { return &dom.EventShadowRootPushed{HostID: 1, Root: &cdp.Node{NodeID: id}} }},
		{"pop shadow", func(_ cdp.NodeID) any { return &dom.EventShadowRootPopped{HostID: 1, RootID: 3} }},
		{"add pseudo", func(id cdp.NodeID) any {
			return &dom.EventPseudoElementAdded{ParentID: 1, PseudoElement: &cdp.Node{NodeID: id}}
		}},
		{"remove pseudo", func(_ cdp.NodeID) any { return &dom.EventPseudoElementRemoved{ParentID: 1, PseudoElementID: 4} }},
		{"scrollable", func(_ cdp.NodeID) any { return &dom.EventScrollableFlagUpdated{NodeID: 1} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := &cdp.Frame{ID: "root"}
			refresh := func() {
				frame.Lock()
				defer frame.Unlock()
				frame.Root = &cdp.Node{NodeID: 1, Invalidated: make(chan struct{}), Children: []*cdp.Node{{NodeID: 2}}, ShadowRoots: []*cdp.Node{{NodeID: 3}}, PseudoElements: []*cdp.Node{{NodeID: 4}}}
				frame.Nodes = make(map[cdp.NodeID]*cdp.Node)
				walk(frame.Nodes, frame.Root)
			}
			refresh()
			target := &Target{frames: map[cdp.FrameID]*cdp.Frame{frame.ID: frame}, cur: frame.ID, errf: t.Errorf}
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Go(func() {
				<-start
				for range 1000 {
					refresh()
				}
			})
			wg.Go(func() {
				<-start
				for i := range 1000 {
					target.domEvent(context.Background(), tc.event(cdp.NodeID(i+5)))
				}
			})
			close(start)
			wg.Wait()
			target.domEvent(context.Background(), &dom.EventAttributeModified{NodeID: 1, Name: "finished", Value: "true"})
			finished := false
			for i := 0; i < len(frame.Root.Attributes); i += 2 {
				finished = finished || frame.Root.Attributes[i] == "finished" && frame.Root.Attributes[i+1] == "true"
			}
			if frame.Nodes[1] != frame.Root || !finished {
				t.Fatal("DOM events did not update the current document")
			}
		})
	}
}

func TestDOMDocumentUpdated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	oldRoot := &cdp.Node{NodeID: 1, Invalidated: make(chan struct{})}
	frame := &cdp.Frame{ID: "root", Root: oldRoot, Nodes: map[cdp.NodeID]*cdp.Node{1: oldRoot}}
	target := &Target{browser: &Browser{cmdQueue: make(chan *cdproto.Message)}, frames: map[cdp.FrameID]*cdp.Frame{frame.ID: frame}, cur: frame.ID, errf: t.Errorf}
	done := make(chan struct{})
	go func() {
		target.domEvent(ctx, &dom.EventDocumentUpdated{})
		close(done)
	}()
	select {
	case cmd := <-target.browser.cmdQueue:
		if cmd.Method != dom.CommandGetDocument {
			t.Fatalf("command = %s, want DOM.getDocument", cmd.Method)
		}
		target.listenersMu.Lock()
		target.listeners = runListeners(target.listeners, &cdproto.Message{ID: cmd.ID, Result: []byte(`{"root":{"nodeId":2,"backendNodeId":2,"nodeType":1,"nodeName":"HTML","localName":"html","nodeValue":""}}`)})
		target.listenersMu.Unlock()
	case <-ctx.Done():
		t.Fatal("document update did not request its new root")
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("document update did not complete")
	}
	select {
	case <-oldRoot.Invalidated:
	default:
		t.Fatal("old document was not invalidated")
	}
	if frame.Root.NodeID != 2 || len(frame.Nodes) != 1 || frame.Nodes[2] != frame.Root {
		t.Fatal("document update did not replace the node map")
	}
}

func TestDOMEventBeforeFrame(t *testing.T) {
	target := &Target{frames: make(map[cdp.FrameID]*cdp.Frame), errf: t.Errorf}
	target.domEvent(context.Background(), &dom.EventAttributeModified{NodeID: 1, Name: "class", Value: "ready"})
	target.domEvent(context.Background(), &dom.EventSetChildNodes{ParentID: 1, Nodes: []*cdp.Node{{NodeID: 2}}})
}

func TestDOMDocumentRefreshDrainsEventsBeforeResponse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	oldRoot := &cdp.Node{NodeID: 1, Invalidated: make(chan struct{})}
	frame := &cdp.Frame{ID: "root", Root: oldRoot, Nodes: map[cdp.NodeID]*cdp.Node{1: oldRoot}}
	target := &Target{browser: &Browser{cmdQueue: make(chan *cdproto.Message)}, messageQueue: make(chan *cdproto.Message, 1024), frames: map[cdp.FrameID]*cdp.Frame{frame.ID: frame}, cur: frame.ID, errf: t.Errorf}
	runDone := make(chan struct{})
	go func() {
		target.run(ctx)
		close(runDone)
	}()
	refreshDone := make(chan struct{})
	go func() {
		target.documentUpdated(ctx)
		close(refreshDone)
	}()
	defer func() {
		cancel()
		<-runDone
		<-refreshDone
	}()
	var request *cdproto.Message
	select {
	case request = <-target.browser.cmdQueue:
		if request.Method != dom.CommandGetDocument {
			t.Fatalf("command = %s, want DOM.getDocument", request.Method)
		}
	case <-ctx.Done():
		t.Fatal("document refresh did not request its new root")
	}
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		// One executing event and one blocked send exceed the 4096-event queue.
		for range 4098 {
			event := &cdproto.Message{Method: "DOM.attributeModified", Params: []byte(`{"nodeId":1,"name":"class","value":"ready"}`)}
			select {
			case target.messageQueue <- event:
			case <-ctx.Done():
				return
			}
		}
		response := &cdproto.Message{ID: request.ID, Result: []byte(`{"root":{"nodeId":2,"backendNodeId":2,"nodeType":1,"nodeName":"HTML","localName":"html","nodeValue":""}}`)}
		select {
		case target.messageQueue <- response:
		case <-ctx.Done():
		}
	}()
	defer func() {
		cancel()
		<-sendDone
	}()
	select {
	case <-refreshDone:
	case <-ctx.Done():
		t.Fatal("DOM event queue prevented delivery of the document response")
	}
	frame.RLock()
	defer frame.RUnlock()
	if frame.Root == nil || frame.Root.NodeID != 2 || len(frame.Nodes) != 1 || frame.Nodes[2] != frame.Root {
		t.Fatal("document response did not replace the node map")
	}
}
