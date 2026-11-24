# MirrorPort

Windows 10/11 x64で、最大4台のiPhoneの画面をAirPlayで同時に受信できます。PCのブラウザーで映像を並べて確認でき、OBSにも端末ごとの映像を追加できます。

## 使い方

1. GitHub Releasesの `MirrorPort-<バージョン>-windows-x64.zip` を展開します。
2. 初回に `setup-airplay.cmd` を実行して受信エンジンを準備します。完了したら `MirrorPort.exe` を開きます。
3. iPhoneとPCを同じネットワークにつなぎます。PCは有線LANでも利用できます。
4. 使う受信機のカードで「受信を開始」を押し、各iPhoneのコントロールセンターの「画面ミラーリング」で対応する受信機名（初期値は **MirrorPort 1**、**MirrorPort 2** など）を選びます。接続コードが求められたら、そのカードに表示された4桁のコードを入力します。[AppleのAirPlay手順](https://support.apple.com/ja-jp/102661)

「受信機を追加」で4台分までカードを表示できます。各カードで受信機名、フレームレート、音声、表示先を変更できます。Windowsへの音声出力は、初期設定で1台目が再生、2台目以降がミュートです。

表示先が「自動」のとき、FFmpegが見つかればブラウザーに表示します。FFmpegがなければWindowsウィンドウが開きます。ブラウザー表示では、横に並べる表示と縦に並べる表示を切り替えられ、各映像を全画面表示またはJPEG保存できます。

FFmpegは PATH、`C:/ffmpeg/bin/ffmpeg.exe`、`engines/ffmpeg/bin/ffmpeg.exe` のいずれかに配置できます。[FFmpegのWindows版配布先](https://ffmpeg.org/download.html#build-windows)

## OBSに取り込む

1. MirrorPortとOBSを同じPCで起動します。FFmpegを用意し、受信機の表示先を「ブラウザー・OBS」または「自動」に設定して受信を開始します。
2. カードの「OBS URLをコピー」を押します。OBSの「ソース」から「ブラウザ」を追加し、「ローカルファイル」をオフにして、そのURLを貼り付けます。[OBSのブラウザソース](https://obsproject.com/kb/browser-source)
3. 縦画面は幅1080・高さ1920、横画面は幅1920・高さ1080を目安に設定します。ソースを追加した台数分だけ繰り返し、OBS上で映像を配置します。

各ソースは映像だけを表示します。余白と未接続時の表示は透明です。既定のURLは `http://127.0.0.1:42884/obs/1` から `http://127.0.0.1:42884/obs/4` で、同じ受信機番号ならMirrorPortを再起動しても使えます。受信機が再接続すると映像が戻ります。

配信に音声を含める場合は、対象の受信機の「Windowsへの音声」を「再生」に設定し、OBSの「音声出力キャプチャ」でPCの出力デバイスを追加します。同じ出力デバイスで再生した端末の音声はまとめて取り込まれます。

### 接続できないとき

- ゲストWi-Fiの端末間通信制限やVPNを確認してください。
- Windowsファイアウォールの許可は、信頼できるプライベートネットワークで `uxplay.exe` に対して行います。AirPlay受信はTCP/UDP 35000–35011（1台につき連続する3ポート）、mDNS検出はUDP 5353を使用します。[UxPlayのトラブルシューティング](https://github.com/FDH2/UxPlay#troubleshooting)
- 受信機を起動できない場合は、操作画面の「受信ログ」を確認してください。別のMirrorPortが受信中なら、その操作画面から受信を停止してください。
- プロテクトされた映像は黒画面になる場合があります。
- 操作画面が開かない場合は、別のMirrorPortやローカルポート42884を使用するアプリが起動していないか確認してください。起動時に `-port <番号>` を指定すると、操作画面とOBS用URLのポートを変更できます。

## 開発と配布

Go 1.24以降が必要です。

```powershell
go test -v ./...
go build -trimpath -o MirrorPort.exe .
powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File .\scripts\package.ps1 -Version v1.0.0
```

最後のコマンドで、配布用ZIPと `SHA256SUMS.txt` が `dist/` に生成されます。ZIPに含まれるのはMirrorPortの実行ファイル、セットアップスクリプト、READMEです。セットアップスクリプトは固定の[AirBridgeリリース](https://github.com/hull-cai/AirBridge-Windows/releases/tag/v0.3.0)からUxPlayの実行環境を取得し、SHA-256を照合してライセンス表示とともに展開します。取得済みの公式ZIPは `setup-airplay.ps1 -ArchivePath <ZIPのパス>` で指定できます。

既存のUxPlayやFFmpegを使う場合は、起動前に `MIRRORPORT_UXPLAY` または `MIRRORPORT_FFMPEG` に対応する実行ファイルの絶対パスを設定できます。配布ZIPとGitの追跡対象から、ローカルに展開した受信エンジンやFFmpegは除外しています。

### GitHub Releases

空のGitHubリポジトリを作成し、次の例の `OWNER/REPOSITORY` を作成したリポジトリに置き換えます。Git管理済みのフォルダーでは、初期化済みの手順を省略できます。

```powershell
git init -b main
git add .
git commit -m "Initial MirrorPort release"
git remote add origin https://github.com/OWNER/REPOSITORY.git
git push -u origin main
```

GitHub ActionsはプッシュとプルリクエストでWindows用ZIPを作り、成果物に保存します。`v1.0.0` のようなタグをプッシュすると、対応するGitHub ReleaseにZIPとSHA-256を添付します。[GitHub Releases](https://docs.github.com/ja/repositories/releasing-projects-on-github/managing-releases-in-a-repository)

```powershell
git tag v1.0.0
git push origin v1.0.0
```
