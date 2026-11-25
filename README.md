# MirrorPort

Windows 10/11 x64で、最大4台のiPhoneの画面をAirPlay、USB直接接続、HDMIキャプチャーで受信できます。PCのブラウザーで映像を並べて確認でき、OBSにも端末ごとの映像を追加できます。

## AirPlayで接続する

1. GitHub Releasesの `MirrorPort-<バージョン>-windows-x64.zip` を展開します。
2. 初回に `setup-airplay.cmd` を実行して受信エンジンを準備します。完了したら `MirrorPort.exe` を開きます。
3. iPhoneとPCを同じネットワークにつなぎます。PCは有線LANでも利用できます。
4. 使う受信機のカードで「受信を開始」を押し、各iPhoneのコントロールセンターの「画面ミラーリング」で対応する受信機名（初期値は **MirrorPort 1**、**MirrorPort 2** など）を選びます。接続コードが求められたら、そのカードに表示された4桁のコードを入力します。[AppleのAirPlay手順](https://support.apple.com/ja-jp/102661)

## USBケーブルで直接接続する

1. MirrorPortのフォルダーで `powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File .\setup-usb.ps1` を実行します。公式ZIPを照合して展開し、USBの補助プログラムを `MirrorPort.exe` の隣に置きます。取得済みの公式ZIPは `-ArchivePath <ZIPのパス>` で指定できます。[iPhoneMirror v1.8.3の配布ページ](https://github.com/RayrenSX/iPhoneMirror/releases/tag/v1.8.3)
2. iPhoneのロックを解除し、データ通信対応のUSBケーブルでPCに接続します。「このコンピュータを信頼しますか？」が表示されたら「信頼」を選びます。WindowsのAppleデバイスアプリにiPhoneが表示されることを確認します。[Appleの信頼設定の手順](https://support.apple.com/ja-jp/109054)
3. 初回は展開フォルダーの `iPhoneMirror.Driver.exe` を開き、対象のiPhoneのキャプチャーフィルタードライバーを準備します。[iPhoneMirrorのドライバー手順](https://github.com/RayrenSX/iPhoneMirror/blob/main/README.en.md#wired-driver-management)
4. MirrorPortで「USBケーブルで直接接続」を選びます。「機器一覧を更新」でiPhoneを選び、「受信を開始」を押します。映像はブラウザー・OBSに、音声はWindowsに出力されます。

USB直接接続にはWi-FiやFFmpegは不要です。USB受信エンジンとドライバーツールはiPhoneMirrorの配布物を別途使います。

## HDMIキャプチャーで接続する

1. iPhoneの映像出力に対応するUSB-CまたはLightningのHDMIアダプターを用意します。iPhoneからHDMIでキャプチャーデバイスにつなぎ、キャプチャーデバイスをPCのUSB端子へ接続します。[Appleの有線接続手順](https://support.apple.com/ja-jp/guide/iphone/iph93af723b9/ios)
2. DirectShow対応のFFmpegを用意してMirrorPortを開き、カードの「接続方法」で「HDMIキャプチャー」を選びます。
3. 「映像デバイス」でキャプチャーデバイスを選びます。音声も取り込む場合は、同じ機器のHDMI音声入力を選びます。「受信を開始」でブラウザーに映像が表示されます。

有線入力はHDMI入力を備えたキャプチャーデバイスを使います。FFmpegはカードで選んだフレームレートを機器に要求し、受け付けない場合は機器の既定モードで再試行します。入力解像度を選んで起動できない場合は「自動」に戻してください。[FFmpegのDirectShow入力仕様](https://ffmpeg.org/ffmpeg-devices.html#dshow)

## 受信中の調整

- カードごとにWindowsへの音量とミュートを変更できます。初期設定で1台目が再生、2台目以降がミュートです。
- 「高度な設定」では入力解像度、JPEG画質、フレームレートなどを変更できます。新しい受信機のフレームレートは60 fpsです。実際のフレームレートは送信端末やキャプチャーデバイスに依存します。設定済みのフレームレートは引き継ぎます。
- AirPlayの既定値は「低遅延」です。映像と音声のタイミングを優先したいときは、受信を停止し、「高度な設定」の「AirPlayの同期」で切り替えます。
- ブラウザー表示中に「モザイク」を押し、映像上をドラッグすると、指定範囲がプレビュー・OBS・画像保存でモザイクになります。範囲はその受信セッション中に有効です。iPhoneを回転させたときは、隠したい位置に合っているか確認してください。

AirPlayの表示先が「自動」のとき、FFmpegが見つかればブラウザーに表示します。FFmpegがなければWindowsウィンドウが開きます。プレビューはグリッド、縦並び、1台を大きく表示するフォーカスに切り替えられます。フォーカス中も各受信機とOBS出力は動作を続けます。各映像は全画面表示またはJPEG保存できます。

FFmpegは PATH、`C:/ffmpeg/bin/ffmpeg.exe`、`engines/ffmpeg/bin/ffmpeg.exe` のいずれかに配置できます。[FFmpegのWindows版配布先](https://ffmpeg.org/download.html#build-windows)

## PCの状態

PC全体のCPU使用率、物理メモリの使用量、GPUの最も忙しいエンジン、最も忙しい物理ディスクを約1秒ごとに更新します。グラフは直近60秒の推移です。Windowsやドライバーが値を提供できない項目は「—」で表示します。測定履歴はMirrorPortの起動中だけ保持します。

## OBSに取り込む

1. MirrorPortとOBSを同じPCで起動し、受信を開始します。AirPlayの場合はFFmpegを用意し、表示先を「ブラウザー・OBS」または「自動」にします。HDMIキャプチャーにもFFmpegが必要です。
2. カードの「OBS URLをコピー」を押します。OBSの「ソース」から「ブラウザ」を追加し、「ローカルファイル」をオフにして、そのURLを貼り付けます。[OBSのブラウザソース](https://obsproject.com/kb/browser-source)
3. 縦画面は幅1080・高さ1920、横画面は幅1920・高さ1080を目安に設定します。ソースを追加した台数分だけ繰り返し、OBS上で映像を配置します。

各ソースは映像だけを表示します。余白と未接続時の表示は透明です。既定のURLは `http://127.0.0.1:42884/obs/1` から `http://127.0.0.1:42884/obs/4` で、同じ受信機番号ならMirrorPortを再起動しても使えます。受信機が再接続すると映像が戻ります。

配信に音声を含める場合は、対象の受信機のミュートを解除し、OBSの「音声出力キャプチャ」でPCの出力デバイスを追加します。同じ出力デバイスで再生した端末の音声はまとめて取り込まれます。

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

最後のコマンドで、配布用ZIPと `SHA256SUMS.txt` が `dist/` に生成されます。ZIPに含まれるのはMirrorPortの実行ファイル、セットアップスクリプト、READMEです。AirPlayのセットアップスクリプトは固定のAirBridgeリリースからUxPlayの実行環境を取得し、SHA-256を照合してライセンス表示とともに展開します。取得済みの公式ZIPは `setup-airplay.ps1 -ArchivePath <ZIPのパス>` で指定できます。[AirBridgeリリース](https://github.com/hull-cai/AirBridge-Windows/releases/tag/v0.3.0)

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
