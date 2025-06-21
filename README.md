运行方式

```
2048.exe --path http.txt --depth 1 --reset false
```
--depth步数
--reset 是否每次运行为新游戏 如果为true的话 每次运行游戏都会重新开始新的一局,
否则的话会延续上一局的分数,比如跑一半想出去吃饭了,关掉电脑,回来之后重新打开
继续运行,分数会继续累加

如果链接失败的话 是IP被cf风控了,换IP或者打开cf或者linuxdo,手动过几次验证,把IP过了就行
http.txt的内容
<img src="images\wechat_2025-06-21_171717_542.png" style="zoom:80%;" />

找到ws请求 选择请求表头,勾选原始,然后全选复制http.txt,记得最后打两个回车换行

默认步数为1,算法部分ai写的 只求能运行即可

<img src="images\wechat_2025-06-21_215516_551.png" style="zoom:75%;" />

<img src="images\wechat_2025-06-21_215935_467.png" style="zoom:75%;" />
使用项目

链接ws:github.com/gorilla/websocket 

优化cpu:go.uber.org/automaxprocs

gemini-2.5-pro
