# awake (Go 版)

MacBook の蓋を閉じてもスリープさせないための CLI ツール `awake` の **Go 移植版**です。

オリジナルは [tanabee/awake](https://github.com/tanabee/awake)（bash 実装、約700行）で、以下の記事で紹介されています。

- [AI 開発のために Mac の蓋を閉じてもスリープしない CLI「awake」を作った](https://zenn.dev/cureapp/articles/2b09dcc8947af9)

## オリジナルとの違い

同じ目的（クラムシェルスリープ抑止・アイドルスリープ抑止・終了時の自動復元）を、bash + `mkfifo` ではなく Go + cgo のネイティブ API 呼び出しで実現しています。

| 機能 | オリジナル (bash) | 本実装 (Go) |
|---|---|---|
| アイドルスリープ抑止 | `caffeinate -is`（外部プロセス） | `IOPMAssertionCreateWithName`（プロセス内蔵、公開API） |
| 発熱監視 (`-s`) | `osascript` 経由で `NSProcessInfo.thermalState` | cgo + Objective-C ブリッジで直接呼び出し（公開API） |
| バッテリー監視 (`-b`) | `pmset -g batt` をパース | `IOPSCopyPowerSourcesInfo`（公開API） |
| クラムシェルスリープ無効化 | `sudo pmset -a disablesleep 1/0` | 同左（`pmset` をそのまま `exec.Command` で呼び出し） |
| 終了時の確実な復元 | `mkfifo` + root helper プロセス | `os.Pipe()` + `cmd.StdinPipe` 相当の root helper プロセス |
| 配布形態 | シェルスクリプト一式 | 単一バイナリ |

`disablesleep` の切り替えには root 権限が必須（`pmset` の制約）なため、オリジナルと同じ設計思想（**起動時の sudo 1 回だけで root 権限を持つ子プロセスを立ち上げっぱなしにし、パイプの EOF をトリガーに終了時の復元を行う**）を Go でも踏襲しています。これにより、`-t` で長時間の自動停止を指定しても、終了時に sudo パスワードを再度求められることはありません。

## 動作要件

- macOS（Intel / Apple Silicon 両対応。cgo で `IOKit` / `Foundation` フレームワークをリンクしていますが、使用しているAPI自体はどちらのアーキテクチャでも利用可能です。ただし主に Apple Silicon 環境での動作を想定・検証しています）
- Xcode Command Line Tools（`clang` と各種フレームワークヘッダが必要）
  ```bash
  xcode-select --install
  ```
- Go 1.21 以降を推奨（`CGO_ENABLED=1` がデフォルトで有効な環境）

## ビルド方法

リポジトリ直下で以下を実行するだけです。

```bash
go build -o awake .
```

単一の `.go` ファイルで完結しているため、`cgo` 用の追加ヘッダファイルなどは不要です。ビルドが成功すると `awake` という単一バイナリが生成されます。

生成したバイナリは任意の `PATH` の通ったディレクトリに配置してください。

```bash
cp awake ~/.local/bin/awake
```

### クロスコンパイルについて

`cgo` を使用しているため、**必ず macOS 上でネイティブビルドしてください**。他 OS からのクロスコンパイルはできません。また、Intel Mac 向けバイナリと Apple Silicon 向けバイナリを相互にクロスビルドすることもできないため、それぞれのアーキテクチャの実機上でビルドする必要があります。

## 使い方

### 基本（フォアグラウンド）

```bash
awake
```

起動時に一度だけ sudo パスワードを求められます。`running` のメッセージが出たら蓋を閉じて OK です。終了する際は `Ctrl+C` を押すと、スリープ設定が自動的に元に戻ります（このとき sudo パスワードは求められません）。

### 時間で自動停止（`-t` / `--timeout`）

```bash
awake -t 1h30m     # 1時間30分後に自動停止
awake -t 3600      # 単位なしの整数は秒として扱う（== 1h）
```

### バッテリー残量で自動停止（`-b` / `--battery`）

```bash
awake -b 20        # バッテリー残量 20% 以下で自動停止
```

AC 電源に接続中はチェックをスキップするため、充電中に意図せず止まることはありません。

### 発熱で自動停止（`-s` / `--safe`）

```bash
awake -s           # 熱圧力が serious 以上の状態が3分続いたら自動停止
```

`NSProcessInfo.thermalState` を60秒ごとにポーリングします。追加ツールやsudoは不要です。

### 組み合わせ

```bash
awake -t 8h -b 20 -s   # いずれか一つでも条件を満たした時点で自動停止
```

## 制約

- **macOS 専用**です（Intel / Apple Silicon 両対応、クロスコンパイルは不可）。
- **sudo が必須**です（`pmset -a disablesleep` が root 権限を要求するため）。起動時の1回のみです。
- **発熱**：蓋を閉じると排熱がこもります。重い CPU/GPU 負荷を長時間かけるのは避けてください。心配な場合は `-s` を併用してください。
- **バッテリー**：蓋を閉じてもスリープしなくなるため、バッテリー駆動では当然消費が続きます。長時間の場合は AC に接続するか `-b` で下限を設定してください。
- OS がクラッシュした場合、root helper ごと終了するため `disablesleep` が `1` のまま残ることがあります。その場合は手動で復元してください。
  ```bash
  sudo pmset -a disablesleep 0
  ```

## 参考

- [オリジナル実装 tanabee/awake (GitHub)](https://github.com/tanabee/awake)
- [AI 開発のために Mac の蓋を閉じてもスリープしない CLI「awake」を作った (Zenn)](https://zenn.dev/cureapp/articles/2b09dcc8947af9)
- [`pmset` man page](https://ss64.com/osx/pmset.html)
- [`caffeinate` man page](https://ss64.com/osx/caffeinate.html)
