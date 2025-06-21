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

var version = "1.0.0" // 版本号

// --- 网络与游戏状态结构体 ---

type ServerMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type GameStateData struct {
	Board    Board  `json:"board"`
	Score    int    `json:"score"`
	GameOver bool   `json:"game_over"`
	Victory  bool   `json:"victory"`
	Message  string `json:"message"`
}

type ClientMessage struct {
	Type string   `json:"type"`
	Data MoveData `json:"data"`
}

type MoveData struct {
	Direction string `json:"direction"`
}

type Board [4][4]int
type Direction int

const (
	Up Direction = iota
	Down
	Left
	Right
)

var directionToString = map[Direction]string{Up: "up", Down: "down", Left: "left", Right: "right"}

// --- AI 核心 (重构后) ---

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

// MemoizationCache 定义一个线程安全的缓存结构
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

// evaluateBoard 评估函数
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
	// 简单的单调性计算
	// (为了完整性，可以添加一个更复杂的单调性计算逻辑)
	return monotonicity*monotonicityWeight +
		smoothness*smoothnessWeight +
		math.Log(float64(emptyCells+1))*emptyCellsWeight +
		float64(maxValue)*maxValueWeight
}

// AI 结构体，封装了所有AI相关的逻辑和状态
type AI struct {
	depth int
	cache *MemoizationCache // AI实例共享一个缓存
}

// NewAI 创建一个新的AI实例
func NewAI(depth int) *AI {
	return &AI{
		depth: depth,
		cache: NewMemoizationCache(), // 在创建AI时初始化缓存
	}
}

// FindBestMove 是AI的公共入口，它并行地为顶层移动寻找最佳选择
func (ai *AI) FindBestMove(b Board) Direction {
	bestScore := -math.MaxFloat64
	bestMove := Up // 默认移动，以防万一

	var wg sync.WaitGroup
	results := make(chan MoveResult, 4)

	var validMoves []Direction
	for _, dir := range []Direction{Up, Down, Left, Right} {
		if _, moved := Move(b, dir); moved {
			validMoves = append(validMoves, dir)
		}
	}

	if len(validMoves) == 0 {
		return bestMove // 没有有效移动，返回默认值
	}

	// 为每个有效的移动启动一个goroutine
	for _, dir := range validMoves {
		wg.Add(1)
		go func(d Direction) {
			defer wg.Done()
			newBoard, _ := Move(b, d)
			// 所有goroutine共享同一个AI实例的缓存
			score := ai.searchExpectationNode(newBoard, ai.depth-1)
			results <- MoveResult{Dir: d, Score: score}
		}(dir)
	}

	// 等待所有goroutine完成，然后关闭channel
	go func() {
		wg.Wait()
		close(results)
	}()

	// 从结果中选出最优解
	for res := range results {
		if res.Score > bestScore {
			bestScore = res.Score
			bestMove = res.Dir
		}
	}

	return bestMove
}

// searchMaxNode (玩家回合): 寻找所有移动中能获得最高分数的那个移动
func (ai *AI) searchMaxNode(b Board, depth int) float64 {
	bestScore := -math.MaxFloat64
	hasMoved := false

	for _, dir := range []Direction{Up, Down, Left, Right} {
		newBoard, moved := Move(b, dir)
		if moved {
			hasMoved = true
			score := ai.searchExpectationNode(newBoard, depth-1)
			if score > bestScore {
				bestScore = score
			}
		}
	}

	if !hasMoved {
		return evaluateBoard(b) // 如果无法移动，返回当前局面的评估值
	}
	return bestScore
}

// searchExpectationNode (电脑回合): 计算电脑随机放置方块后的期望分数
func (ai *AI) searchExpectationNode(b Board, depth int) float64 {
	if depth == 0 {
		return evaluateBoard(b)
	}

	flatKey := b.flattenBoard()
	if score, found := ai.cache.Get(flatKey); found {
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
		return evaluateBoard(b) // 没有空格，游戏即将结束
	}

	var totalScore2, totalScore4 float64

	// 计算所有空位放置'2'后的总分数
	tempBoard := b
	for _, cell := range emptyCells {
		tempBoard[cell.r][cell.c] = 2
		totalScore2 += ai.searchMaxNode(tempBoard, depth)
		tempBoard[cell.r][cell.c] = 0 // 恢复棋盘
	}

	// 计算所有空位放置'4'后的总分数
	for _, cell := range emptyCells {
		tempBoard[cell.r][cell.c] = 4
		totalScore4 += ai.searchMaxNode(tempBoard, depth)
		tempBoard[cell.r][cell.c] = 0 // 恢复棋盘
	}

	// 计算期望值
	avgScore2 := totalScore2 / float64(len(emptyCells))
	avgScore4 := totalScore4 / float64(len(emptyCells))

	finalScore := 0.9*avgScore2 + 0.1*avgScore4
	ai.cache.Set(flatKey, finalScore)

	return finalScore
}

// --- Main Application Logic (主程序逻辑) ---
func main() {
	url := flag.String("path", "http.txt", "http文本路径")
	depth := flag.Int("depth", 4, "AI 决策深度，推荐4-6。默认为4")
	reset := flag.Bool("reset", false, "是否每次运行开启新的一局")
	flag.Parse()

	fmt.Printf("2048 死算 版本: %s\n", version)
	httpRequestText, err := os.ReadFile(*url)
	if err != nil {
		log.Fatalf("读取HTTP请求文件 '%s' 失败: %v", *url, err)
	}
	req, err := parseHTTPRequest(string(httpRequestText))
	if err != nil {
		log.Fatalf("解析HTTP请求错误: %v\n", err)
	}

	log.Printf("正在连接到 url: %s", req.URL.String())
	log.Printf("AI决策深度设置为: %d", *depth)

	// --- AI 实例创建 ---
	ai := NewAI(*depth)
	// 创建一个channel来传递最新的游戏状态
	// 缓冲区大小为1，意味着我们只关心最新的状态
	gameStateChan := make(chan GameStateData, 1)
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	headers := http.Header{}
	headers.Add("User-Agent", req.Header.Get("User-Agent"))
	headers.Add("Cookie", req.Header.Get("Cookie"))
	headers.Add("Origin", req.Header.Get("Origin"))

	c, res, err := websocket.DefaultDialer.Dial(req.URL.String(), headers)
	if err != nil {
		if res != nil {
			log.Println("连接失败，响应头:")
			for k, v := range res.Header {
				log.Printf("%s: %s", k, v)
			}
		}
		log.Fatalf("连接失败: %v", err)
	}
	defer c.Close()

	log.Println("连接成功！")
	if *reset {
		log.Println("发送新游戏请求...")
		c.WriteMessage(websocket.TextMessage, []byte(`{"type":"new_game","data":{}}`))
	}

	done := make(chan struct{})

	// --- Goroutine 1: 读取器 (生产者) ---
	// 这个goroutine只负责从websocket读取消息并放入channel
	go func() {
		defer close(done)
		for {
			_, message, err := c.ReadMessage()
			if err != nil {
				log.Println("读取器: 读取消息失败:", err)
				close(gameStateChan) // 关闭channel以通知处理器退出
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
				// 使用非阻塞的方式发送到channel
				// 如果channel已满，先清空再放入最新的
				select {
				case <-gameStateChan: // 清空旧的
				default:
				}
				gameStateChan <- gameState // 放入最新的
			}
		}
	}()
	// --- Goroutine 2: 处理器 (消费者) ---
	// 这个goroutine负责处理游戏逻辑和AI计算
	go func() {
		var currentScore int
		for gameState := range gameStateChan { // 从channel中读取数据
			currentScore = gameState.Score
			if gameState.GameOver || gameState.Message == "Game is already finished" {
				log.Println("处理器: 游戏结束!", "最终得分:", currentScore)
				c.WriteMessage(websocket.TextMessage, []byte(`{"type":"new_game","data":{}}`))
				log.Println("处理器: 已发送重置新对局请求")
				time.Sleep(1 * time.Second)
				interrupt <- os.Interrupt
				return // 结束处理器goroutine
			}

			fmt.Printf("\n--- 收到新状态 | 分数: %d ---\n", gameState.Score)
			gameState.Board.printBoard()
			// 后续的AI计算和发送逻辑与之前完全相同
			startTime := time.Now()
			var bestDir Direction
			if gameState.Score < 20 {
				log.Println("AI 决策: 随机移动")
				possibleDirs := []Direction{Up, Down, Left, Right}
				bestDir = possibleDirs[rand.Intn(len(possibleDirs))]
			} else {
				log.Println("AI 正在并行计算最佳移动...")
				bestDir = ai.FindBestMove(gameState.Board)
				log.Printf("AI 决策: %s", directionToString[bestDir])
			}
			duration := time.Since(startTime)
			log.Printf("计算耗时: %s", duration)
			moveJSON, err := moveToJson(bestDir)
			if err != nil {
				log.Println("创建移动指令失败:", err)
				continue
			}
			if duration < 100*time.Millisecond {
				wait := 100*time.Millisecond - duration
				log.Printf("等待 %s 以满足服务器要求", wait)
				time.Sleep(wait)
			}
			log.Printf("发送移动指令: %s", string(moveJSON))
			if err := c.WriteMessage(websocket.TextMessage, moveJSON); err != nil {
				log.Println("发送移动指令失败:", err)
				return
			}
		}
	}()

	<-interrupt
	log.Println("收到中断信号，正在关闭连接...")
	c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	time.Sleep(time.Second) // 等待关闭消息发送
}

// --- 游戏逻辑与辅助函数 ---

func moveToJson(direction Direction) ([]byte, error) {
	moveStr := directionToString[direction]
	moveCmd := ClientMessage{
		Type: "move",
		Data: MoveData{Direction: moveStr},
	}
	return json.Marshal(moveCmd)
}

func parseHTTPRequest(text string) (*http.Request, error) {
	return http.ReadRequest(bufio.NewReader(strings.NewReader(text)))
}

func (b *Board) printBoard() {
	fmt.Println("-----------------------------")
	for _, row := range *b {
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

func moveLeft(b Board) (Board, int) {
	var newBoard Board
	score := 0
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
				score += line[i]
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
	return newBoard, score
}

func Move(b Board, dir Direction) (Board, bool) {
	originalBoard := b
	var newBoard Board

	switch dir {
	case Left:
		newBoard, _ = moveLeft(b)
	case Right:
		newBoard, _ = moveLeft(reverse(b))
		newBoard = reverse(newBoard)
	case Up:
		newBoard, _ = moveLeft(transpose(b))
		newBoard = transpose(newBoard)
	case Down:
		newBoard, _ = moveLeft(reverse(transpose(b)))
		newBoard = transpose(reverse(newBoard))
	}
	return newBoard, newBoard != originalBoard
}
