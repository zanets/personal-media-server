# Personal Media Server（映格 FRAME）

Go + React + SQLite。本機硬碟或掛載的 NAS 提供影音，手機、筆電透過家中區網瀏覽器串流觀看。React 與前端資源都內嵌在 Go 執行檔中，使用時不需要 Node.js、Python、CDN 或外網。

## 取得與啟動

需要 Go 1.25 或以上。首次編譯需連網下載依賴：

```bash
git clone https://github.com/zanets/personal-media-server.git
cd personal-media-server
go build -o frame .
./frame --media "/你的影音資料夾"
```

先試介面，可使用專案的空白媒體資料夾：

```bash
./frame --media ./media
```

本機開啟 **http://localhost:8765**。手機、筆電連同一區網，開啟 **http://伺服器區網IP:8765**。Linux 可用 `hostname -I` 查 IP。防火牆需允許家中區網存取 TCP 8765，執行伺服器的設備需保持開機。Ctrl+C 停止。

### 多個資料夾、NAS、資料庫

```bash
./frame --media "/home/user/Videos" --media "/mnt/nas/Music" --db "/home/user/frame-data/library.sqlite3" --port 8765
```

NAS 資料夾需先掛載至作業系統。資料庫的上層資料夾需已存在且可寫入。資料庫預設在啟動時的工作目錄；固定使用相同 `--db` 絕對路徑可以避免換目錄後讀到另一份資料庫。

只允許本機存取：加 `--host 127.0.0.1`。預設 `--host 0.0.0.0` 接受區網連線。

## 使用方式

1. 啟動時掃描指定資料夾，也可在介面按「重新掃描」加入新影音。
2. 點影音卡片，使用瀏覽器播放器播放、調音量、倍速、拖曳或全螢幕；可用選項依瀏覽器而異。
3. 點 1～5 顆星評分，或清除評分。
4. 在標籤欄輸入逗號分隔的分類並儲存，左側可按標籤篩選。
5. 播放至指定位置，輸入筆記並加入時間點；按筆記時間可跳轉，也可刪除筆記。
6. 搜尋片名、標籤、筆記；依加入時間、星等、名稱排序。
7. 播放進度每約 5 秒及暫停／關閉播放器時保存，正常播完重設為 0。不同裝置共用同一份資料。

## 從原始碼編譯

需要 Go 1.25 或以上；初次建置需下載 SQLite 模組。執行時無外部套件依賴，SQLite 不需要獨立伺服器或 C 編譯器。

```bash
go build -o frame .
go test -race ./...
go vet ./...
```

交叉編譯範例：

```bash
# Windows x86_64
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o frame.exe .
# Linux ARM64 NAS
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o frame-linux-arm64 .
# macOS Apple Silicon
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o frame-macos-arm64 .
```

Windows 啟動例：`frame.exe --media "D:\Videos"`。這些其他平台的產物需在對應設備驗證。

## 第一版範圍

- **僅供可信任的私人區網**：尚未提供登入、HTTPS 或外出連線。請勿直接將 8765 轉發至網際網路。
- 可索引 MP4、WebM、M4V、MOV、MKV、AVI、MP3、M4A、WAV、OGG、FLAC；**能否播放取決於瀏覽器與影音編碼**，第一版沒有轉碼。MKV／AVI 等並不保證可播。不支援時會顯示提示。
- 不上傳、不搬動、不刪除原始影音。掃描時不跟隨資料夾捷徑／符號連結，也不提供存取已指定資料夾以外的檔案。
- 尚無縮圖擷取、區間筆記、檔案重新關聯、失效項目清理、線上備份匯入匯出。
- 檔案改名或搬移後可能留下無法播放的舊項目。重新掃描會以新位置建立項目；原筆記不會自動轉移。
- 同時在不同裝置編輯同一欄位，以最後成功寫入為準。
- 備份：先停止伺服器，再複製 `library.sqlite3`；影音另行備份。

前端的 React 18.3.1 授權文件保存在 `dist/vendor/`。前端原始碼無需建置步驟，修改後重新執行 `go build` 即可內嵌最新資源。
