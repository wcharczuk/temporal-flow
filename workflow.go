package temporalflow

import (
	"context"
	"log/slog"

	"github.com/wcharczuk/go-incr"
	"go.temporal.io/sdk/workflow"
)

type Workflow struct{}

var (
	SignalStabilize   = "flow_signal_stabilize"
	SignalSetVariable = "flow_signal_set_variable"
	QueryValues       = "flow_query_values"
	QueryGraph        = "flow_query_graph"
)

type SignalSetVariableData struct {
	NodeID    incr.Identifier
	NodeLabel string
	Value     any
}

func (w Workflow) HostGraph(ctx workflow.Context, graph SerializedGraph) error {
	flowGraph, err := graph.FlowGraph()
	if err != nil {
		return err
	}
	start := workflow.Now(ctx)
	workflow.GetLogger(ctx).Info("host graph workflow starting")
	defer func() {
		workflow.GetLogger(ctx).Info("host graph workflow exiting", slog.Duration("elapsed", workflow.Now(ctx).Sub(start)))
	}()
	if err = workflow.SetQueryHandler(ctx, QueryValues, func() (outputValues map[string]any, err error) {
		outputValues = make(map[string]any)
		for _, obs := range flowGraph.Observers {
			labelOrID := obs.Node().Label()
			if labelOrID == "" {
				labelOrID = obs.Node().ID().String()
			}
			outputValues[labelOrID] = obs.Value()
		}
		return
	}); err != nil {
		return err
	}
	if err = workflow.SetQueryHandler(ctx, QueryGraph, func() (outputGraph SerializedGraph, err error) {
		outputGraph = flowGraph.Serialize()
		return
	}); err != nil {
		return err
	}
	signalStabilizeChannel := workflow.GetSignalChannel(ctx, SignalStabilize)
	signalSetVariableChannel := workflow.GetSignalChannel(ctx, SignalSetVariable)
	var shouldExit bool
	for !shouldExit {
		sel := workflow.NewSelector(ctx)
		sel.AddReceive(signalStabilizeChannel, func(r workflow.ReceiveChannel, _ bool) {
			_ = r.Receive(ctx, nil)
			err = flowGraph.Graph.Stabilize(WithWorkflowContext(context.Background(), ctx))
			if err != nil {
				workflow.GetLogger(ctx).Error("stabilization error", slog.Any("err", err))
				shouldExit = true
			}
		})
		sel.AddReceive(signalSetVariableChannel, func(setVariableChannel workflow.ReceiveChannel, _ bool) {
			var data SignalSetVariableData
			_ = setVariableChannel.Receive(ctx, &data)
			nodeID := data.NodeID
			if data.NodeID.IsZero() {
				nodeID, _ = flowGraph.NodeLabelLookup[data.NodeLabel]
			}
			if nodeID.IsZero() {
				workflow.GetLogger(ctx).Info("signal set value; nodeID is zero, cannot continue", slog.String("nodeID", data.NodeID.Short()), slog.String("nodeLabel", data.NodeLabel))
				return
			}
			if foundNode, ok := flowGraph.NodeLookup[nodeID]; ok {
				if typed, ok := foundNode.(incr.VarIncr[any]); ok {
					typed.Set(data.Value)
					workflow.GetLogger(ctx).Info("signal set value; set successfully", slog.String("nodeID", data.NodeID.Short()), slog.String("nodeLabel", data.NodeLabel))
				} else {
					workflow.GetLogger(ctx).Error("signal set value; node is not a variable node", slog.String("nodeID", data.NodeID.Short()), slog.String("nodeLabel", data.NodeLabel))
				}
			} else {
				workflow.GetLogger(ctx).Error("signal set value; node with identifier not found", slog.String("nodeID", data.NodeID.Short()), slog.String("nodeLabel", data.NodeLabel))
			}
		})
		sel.AddReceive(ctx.Done(), func(_ workflow.ReceiveChannel, _ bool) {
			shouldExit = true
		})
		sel.Select(ctx)
	}
	return nil
}

func (w Workflow) parallelRecompute(ctx workflow.Context, graph *FlowGraph) (err error) {
	stabilizeCtx := WithWorkflowContext(context.Background(), ctx)
	eg := incr.ExpertGraph(graph.Graph)
	if err = eg.EnsureNotStabilizing(stabilizeCtx); err != nil {
		return
	}
	stabilizeCtx = eg.StabilizeStart(stabilizeCtx)
	defer func() {
		eg.StabilizeEnd(stabilizeCtx, err)
	}()
	if incr.ExpertGraph(graph.Graph).RecomputeHeapLen() == 0 {
		return
	}

	var immediateRecompute []INode

	parallelRecomputeNode := func(ctx context.Context, n incr.INode) (err error) {
		err = eg.Recompute(ctx, n, true)
		if incr.ExpertNode(n).Always() {
			immediateRecompute = append(immediateRecompute, n)
		}
		return
	}

	/*
		var immediateRecompute []INode
		var immediateRecomputeMu sync.Mutex
		parallelRecomputeNode := func(ctx context.Context, n INode) (err error) {
			err = graph.recompute(ctx, n, true)
			if n.Node().always {
				immediateRecomputeMu.Lock()
				immediateRecompute = append(immediateRecompute, n)
				immediateRecomputeMu.Unlock()
			}
			return
		}

		var iter recomputeHeapListIter
		for graph.recomputeHeap.len() > 0 {
			graph.recomputeHeap.setIterToMinHeight(&iter)
			err = parallelBatch(ctx, parallelRecomputeNode, iter.Next, graph.parallelism)
			if err != nil {
				break
			}
		}
		if err != nil {
			if graph.clearRecomputeHeapOnError {
				aborted := graph.recomputeHeap.clear()
				for _, node := range aborted {
					for _, ah := range node.Node().onAbortedHandlers {
						ah(ctx, err)
					}
				}
			}
		}
		if len(immediateRecompute) > 0 {
			graph.recomputeHeap.mu.Lock()
			for _, n := range immediateRecompute {
				if n.Node().heightInRecomputeHeap == HeightUnset {
					graph.recomputeHeap.addNodeUnsafe(n)
				}
			}
			graph.recomputeHeap.mu.Unlock()
		}

	*/

	return nil
}
