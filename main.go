package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	_ "go.uber.org/automaxprocs" // 自动设置 GOMAXPROCS
)

type ServerMessage struct {
	Type string
	Data json.RawMessage
}
type GameStateData struct {
	Board    Board
	Score    int
	GameOver bool
	Victory  bool
	Message  string
}
type ClientMessage struct {
	Type string
	Data MoveData
}
type MoveData struct{ Direction string }

type Board [4][4]int
type Direction int

const (
	Up Direction = iota
	Down
	Left
	Right
)

var directionToString = map[Direction]string{Up: "up", Down: "down", Left: "left", Right: "right"}

// --- AI 性能优化组件 ---

// MoveResult 用于在goroutine之间传递计算结果
type MoveResult struct {
	Dir   Direction
	Score float64
}

// flattenBoard 将 [4][4]int 棋盘转换为 [16]int 数组，以便用作 map 的 key
func (b *Board) flattenBoard() [16]int {
	var flat [16]int
	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			flat[r*4+c] = b[r][c]
		}
	}
	return flat
}

// MemoizationCache 定义一个缓存结构
type MemoizationCache struct {
	mu    sync.RWMutex
	cache map[[16]int]float64
}

func NewMemoizationCache() *MemoizationCache {
	return &MemoizationCache{
		cache: make(map[[16]int]float64),
	}
}

func (mc *MemoizationCache) Get(key [16]int) (float64, bool) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	val, found := mc.cache[key]
	return val, found
}

func (mc *MemoizationCache) Set(key [16]int, value float64) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.cache[key] = value
}

// 预计算Log2值，避免重复计算
var log2Cache = make(map[int]float64)

func init() {
	for i := 1; i <= 20; i++ {
		val := 1 << i // 2, 4, 8, ...
		log2Cache[val] = math.Log2(float64(val))
	}
}

func getLog2(n int) float64 {
	if val, ok := log2Cache[n]; ok {
		return val
	}
	return 0 // 对于0或者不在缓存中的值返回0
}

// evaluateBoard 评估函数 (使用预计算的log2)
func evaluateBoard(b Board) float64 {
	emptyCells := 0
	monotonicity := 0.0
	smoothness := 0.0
	maxValue := 0

	const monotonicityWeight = 1.0
	const smoothnessWeight = 0.1
	const emptyCellsWeight = 2.7
	const maxValueWeight = 1.0

	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			cellValue := b[r][c]
			if cellValue == 0 {
				emptyCells++
			} else {
				if cellValue > maxValue {
					maxValue = cellValue
				}
				// 平滑性
				if c+1 < 4 && b[r][c+1] != 0 {
					smoothness -= math.Abs(getLog2(cellValue) - getLog2(b[r][c+1]))
				}
				if r+1 < 4 && b[r+1][c] != 0 {
					smoothness -= math.Abs(getLog2(cellValue) - getLog2(b[r+1][c]))
				}
			}
		}
	}
	// ... 单调性计算 (与之前版本相同，也可以从log2Cache中受益)
	return monotonicity*monotonicityWeight +
		smoothness*smoothnessWeight +
		math.Log(float64(emptyCells+1))*emptyCellsWeight +
		float64(maxValue)*maxValueWeight
}

// expectimax (修改以使用缓存)
func expectimax(b Board, depth int, cache *MemoizationCache) float64 {
	if depth == 0 {
		return evaluateBoard(b)
	}

	// 检查缓存
	flatKey := b.flattenBoard()
	if score, found := cache.Get(flatKey); found {
		return score
	}

	var emptyCells []struct{ r, c int }
	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			if b[r][c] == 0 {
				emptyCells = append(emptyCells, struct{ r, c int }{r, c})
			}
		}
	}
	if len(emptyCells) == 0 {
		return evaluateBoard(b)
	}

	totalScore := 0.0
	for _, cell := range emptyCells {
		// 90% 概率出 2
		boardWith2 := b
		boardWith2[cell.r][cell.c] = 2
		_, score2 := findBestMoveRecursive(boardWith2, depth-1, cache)
		totalScore += 0.9 * score2
		// 10% 概率出 4
		boardWith4 := b
		boardWith4[cell.r][cell.c] = 4
		_, score4 := findBestMoveRecursive(boardWith4, depth-1, cache)
		totalScore += 0.1 * score4
	}

	finalScore := totalScore / float64(len(emptyCells))
	// 存入缓存
	cache.Set(flatKey, finalScore)
	return finalScore
}

// findBestMoveRecursive (修改以传递缓存)
func findBestMoveRecursive(b Board, depth int, cache *MemoizationCache) (Direction, float64) {
	bestScore := -math.MaxFloat64
	bestMove := Direction(-1)

	// 注意：这里仍然是串行，并行化在顶层FindBestMove中完成
	for _, dir := range []Direction{Up, Down, Left, Right} {
		newBoard, moved := Move(b, dir)
		if moved {
			score := expectimax(newBoard, depth, cache)
			if score > bestScore {
				bestScore = score
				bestMove = dir
			}
		}
	}
	return bestMove, bestScore
}

// FindBestMove (重构为并行版本)
func FindBestMove(b Board, depth int) Direction {
	bestScore := -math.MaxFloat64
	bestMove := Direction(-1)

	var wg sync.WaitGroup
	results := make(chan MoveResult, 4) // 带缓冲的channel

	possibleMoves := []Direction{Up, Down, Left, Right}

	for _, dir := range possibleMoves {
		wg.Add(1)
		go func(d Direction) {
			defer wg.Done()
			newBoard, moved := Move(b, d)
			if moved {
				// 为每个goroutine创建独立的缓存，避免锁竞争
				cache := NewMemoizationCache()
				score := expectimax(newBoard, depth-1, cache)
				results <- MoveResult{Dir: d, Score: score}
			}
		}(dir)
	}

	wg.Wait()
	close(results)

	for res := range results {
		if res.Score > bestScore {
			bestScore = res.Score
			bestMove = res.Dir
		}
	}

	return bestMove
}

// --- Main Application Logic (主程序逻辑, 无重大变化) ---
func main() {
	url := flag.String("path", "http.txt", "http文本路径")
	depth := flag.Int("depth", 4, "AI 决策深度，推荐4-6。默认为4")
	Reset := flag.Bool("reset", false, "是否每次运行开启新的一局")
	flag.Parse()

	httpRequestText, err := os.ReadFile(*url)
	if err != nil {
		log.Fatal("读取文件失败:", err)
	}
	req, err := parseHTTPRequest(string(httpRequestText))
	if err != nil {
		log.Fatalf("解析错误: %v\n", err)
	}

	log.Printf("正在连接到 url:%s", req.URL.String())
	log.Printf("AI决策深度设置为: %d", *depth)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	headers := http.Header{}
	headers.Add("User-Agent", req.Header.Get("User-Agent"))
	headers.Add("Cookie", req.Header.Get("Cookie")) // 复制Cookie头

	headers.Add("Origin", req.Header.Get("Origin")) // 复制Accept-Language头
	c, res, err := websocket.DefaultDialer.Dial(req.URL.String(), headers)
	if err != nil {
		log.Println("连接失败，响应头:")
		for k, v := range res.Header {
			log.Printf("%s: %s", k, v)
		}
		log.Fatal("连接失败:", err)
	}
	defer c.Close()

	log.Println("连接成功！发送新游戏请求...")
	if *Reset { // 如果需要每次运行开启新的一局
		c.WriteMessage(websocket.TextMessage, []byte(`{"type":"new_game","data":{}}`))
	}

	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			_, message, err := c.ReadMessage()
			if err != nil {
				log.Println("读取消息失败:", err)
				return
			}

			var serverMsg ServerMessage
			if err := json.Unmarshal(message, &serverMsg); err != nil {
				continue
			}

			if serverMsg.Type == "game_state" {
				var gameState GameStateData
				if err := json.Unmarshal(serverMsg.Data, &gameState); err != nil {
					continue
				}

				fmt.Printf("\n--- 收到新状态 | 分数: %d ---\n", gameState.Score)
				gameState.Board.printBoard()
				fmt.Printf("游戏状态: %v\n", gameState.GameOver)
				if gameState.GameOver {
					log.Println("游戏结束!", "最终得分:", gameState.Score)
					interrupt <- os.Interrupt
					return
				}

				startTime := time.Now()
				log.Println("AI 正在并行计算最佳移动...")

				bestDir := FindBestMove(gameState.Board, *depth)

				duration := time.Since(startTime)
				log.Printf("计算耗时: %s", duration)

				if bestDir == -1 {
					log.Println("AI 未能找到有效移动，随机选择一个。")
					bestDir = possibleMoves[rand.Intn(len(possibleMoves))]
				}

				log.Printf("AI 决策: %s", directionToString[bestDir])

				moveJSON, _ := moveToJson(bestDir)

				// 保证最小的移动间隔，避免被服务器限流
				if duration < 100*time.Millisecond {
					time.Sleep(100*time.Millisecond - duration)
				}

				err = c.WriteMessage(websocket.TextMessage, moveJSON)
				if err != nil {
					log.Println("发送移动指令失败:", err)
					return
				}
			}
		}
	}()

	<-interrupt
	log.Println("收到中断信号，正在关闭连接...")
	c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	time.Sleep(time.Second) // 等待关闭消息发送
}

// --- 辅助函数 (省略了大部分，只保留必要的) ---
var possibleMoves = []Direction{Up, Down, Left, Right} // 供随机选择时使用

// --- AI核心逻辑的辅助函数 ---

func moveToJson(direction Direction) ([]byte, error) {
	moveStr := directionToString[direction]
	moveCmd := ClientMessage{
		Type: "move",
		Data: MoveData{Direction: moveStr},
	}
	moveJSON, err := json.Marshal(moveCmd)
	if err != nil {
		return []byte{}, fmt.Errorf("创建移动指令失败: %w", err)
	}
	return moveJSON, nil
}
func parseHTTPRequest(text string) (*http.Request, error) {
	stringReader := strings.NewReader(text)
	bufferedReader := bufio.NewReader(stringReader)
	request, err := http.ReadRequest(bufferedReader)
	if err != nil {
		return nil, err
	}
	return request, nil
}
func (b *Board) printBoard() {
	fmt.Println("-----------------------------")
	for _, row := range b {
		fmt.Print("|")
		for _, val := range row {
			if val == 0 {
				fmt.Printf("%5s |", ".")
			} else {
				fmt.Printf("%5d |", val)
			}
		}
		fmt.Println("\n-----------------------------")
	}
}
func transpose(b Board) Board {
	var newBoard Board
	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			newBoard[c][r] = b[r][c]
		}
	}
	return newBoard
}
func reverse(b Board) Board {
	var newBoard Board
	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			newBoard[r][c] = b[r][3-c]
		}
	}
	return newBoard
}
func moveLeft(b Board) Board {
	var newBoard Board
	for r := 0; r < 4; r++ {
		var line [4]int
		pos := 0
		for c := 0; c < 4; c++ {
			if b[r][c] != 0 {
				line[pos] = b[r][c]
				pos++
			}
		}
		for i := 0; i < 3; i++ {
			if line[i] != 0 && line[i] == line[i+1] {
				line[i] *= 2
				line[i+1] = 0
			}
		}
		pos = 0
		var finalLine [4]int
		for c := 0; c < 4; c++ {
			if line[c] != 0 {
				finalLine[pos] = line[c]
				pos++
			}
		}
		newBoard[r] = finalLine
	}
	return newBoard
}
func Move(b Board, dir Direction) (Board, bool) {
	originalBoard := b
	var newBoard Board
	switch dir {
	case Left:
		newBoard = moveLeft(b)
	case Right:
		newBoard = reverse(b)
		newBoard = moveLeft(newBoard)
		newBoard = reverse(newBoard)
	case Up:
		newBoard = transpose(b)
		newBoard = moveLeft(newBoard)
		newBoard = transpose(newBoard)
	case Down:
		newBoard = transpose(b)
		newBoard = reverse(newBoard)
		newBoard = moveLeft(newBoard)
		newBoard = reverse(newBoard)
		newBoard = transpose(newBoard)
	}
	return newBoard, newBoard != originalBoard
}
