package main

import "testing"

func TestMoveLeft(t *testing.T) {
	board := Board{
		{2, 0, 2, 4},
		{4, 4, 4, 0},
		{0, 0, 0, 0},
		{2, 2, 2, 2},
	}

	got, score := moveLeft(board)
	want := Board{
		{4, 4, 0, 0},
		{8, 4, 0, 0},
		{0, 0, 0, 0},
		{4, 4, 0, 0},
	}

	if got != want {
		t.Fatalf("moveLeft board mismatch\nwant=%v\ngot=%v", want, got)
	}

	if score != 20 {
		t.Fatalf("moveLeft score mismatch: want=20 got=%d", score)
	}
}

func TestMoveDirections(t *testing.T) {
	board := Board{
		{2, 0, 0, 0},
		{2, 0, 0, 0},
		{4, 0, 0, 0},
		{4, 0, 0, 0},
	}

	upBoard, upMoved := Move(board, Up)
	if !upMoved {
		t.Fatalf("expected Up to move")
	}
	if upBoard[0][0] != 4 || upBoard[1][0] != 8 {
		t.Fatalf("unexpected Up result: %v", upBoard)
	}

	rightBoard, rightMoved := Move(board, Right)
	if !rightMoved {
		t.Fatalf("expected Right to move")
	}
	if rightBoard[0][3] != 2 || rightBoard[1][3] != 2 || rightBoard[2][3] != 4 || rightBoard[3][3] != 4 {
		t.Fatalf("unexpected Right result: %v", rightBoard)
	}
}

func TestSearchExpectationNodeCacheRespectsDepth(t *testing.T) {
	ai := NewAI(4)
	board := Board{
		{2, 4, 8, 16},
		{32, 64, 128, 0},
		{2, 4, 8, 16},
		{32, 64, 0, 0},
	}

	_ = ai.searchExpectationNode(board, 1)
	_ = ai.searchExpectationNode(board, 2)

	if len(ai.cache.cache) < 2 {
		t.Fatalf("expected separate cache entries for different depths, got %d", len(ai.cache.cache))
	}
}

func TestGetValidMoves(t *testing.T) {
	board := Board{
		{2, 4, 2, 4},
		{4, 2, 4, 2},
		{2, 4, 2, 4},
		{0, 2, 4, 2},
	}

	moves := getValidMoves(board)
	if len(moves) == 0 {
		t.Fatalf("expected at least one valid move")
	}
}

func TestCornerBonusPrefersMaxTileInCorner(t *testing.T) {
	cornerBoard := Board{
		{128, 64, 32, 16},
		{8, 4, 2, 0},
		{0, 0, 0, 0},
		{0, 0, 0, 0},
	}
	centerBoard := Board{
		{64, 32, 16, 8},
		{4, 128, 2, 0},
		{0, 0, 0, 0},
		{0, 0, 0, 0},
	}

	cornerScore := evaluateBoard(cornerBoard)
	centerScore := evaluateBoard(centerBoard)
	if cornerScore <= centerScore {
		t.Fatalf("expected corner board to evaluate higher, got corner=%f center=%f", cornerScore, centerScore)
	}
}

func TestDynamicDepth(t *testing.T) {
	sparseBoard := Board{
		{2, 0, 0, 0},
		{0, 0, 0, 0},
		{0, 0, 0, 0},
		{0, 0, 0, 0},
	}
	denseBoard := Board{
		{2, 4, 8, 16},
		{32, 64, 128, 256},
		{512, 1024, 2, 4},
		{8, 16, 0, 0},
	}

	if got := dynamicDepth(sparseBoard, 4); got != 3 {
		t.Fatalf("expected sparse board depth 3, got %d", got)
	}
	if got := dynamicDepth(denseBoard, 4); got != 5 {
		t.Fatalf("expected dense board depth 5, got %d", got)
	}
}

func TestEvaluateBoardPenalizesDangerouslyLowMoveBoards(t *testing.T) {
	flexibleBoard := Board{
		{2, 2, 4, 8},
		{16, 32, 64, 128},
		{0, 0, 0, 0},
		{0, 0, 0, 0},
	}
	dangerBoard := Board{
		{2, 4, 8, 16},
		{32, 64, 128, 256},
		{2, 4, 8, 16},
		{32, 64, 128, 0},
	}

	if len(getValidMoves(dangerBoard)) > len(getValidMoves(flexibleBoard)) {
		t.Fatalf("expected danger board to have fewer or equal moves")
	}
	if evaluateBoard(flexibleBoard) <= evaluateBoard(dangerBoard) {
		t.Fatalf("expected flexible board to score higher than danger board")
	}
}
