# oc-mirctl

稼働中の OpenShift クラスタから disconnected インストールに必要な情報を収集し、mirror 用の設定ファイル生成・ミラーリング実行・クラスタへの適用までを行う CLI ツール。

## Install

### go install

```bash
go install github.com/konono/oc-mirctl/cmd/oc-mirctl@latest
# → $(go env GOPATH)/bin/oc-mirctl (GOBIN を設定している場合はそこ)
```

`oc-mirctl --version` には、モジュールのバージョン (タグが無い場合は `v0.0.0-<時刻>-<コミット>` 形式) が表示される。

### ソースからビルド

```bash
make build
# → bin/oc-mirctl

# PATH に入れる場合
make install
```

### クロスビルド

```bash
make cross
# → bin/oc-mirctl-linux-amd64
# → bin/oc-mirctl-linux-arm64
# → bin/oc-mirctl-darwin-arm64
```

## データディレクトリ構造

すべてのデータは `mirror_data/` 配下にクラスタ名のサブディレクトリとして保存される。
ベースディレクトリ名は `mirror_data` で固定されており、`--output-dir` のようなオプションは存在しない。

```
mirror_data/                          # 固定ベースディレクトリ (変更不可)
  └─ <cluster-name>/                  # collect 時にクラスタ名で自動作成
      ├── collected-data.json         # 収集データ (collect が毎回上書き)
      ├── exclusions.json             # 除外リスト (exclude/include が管理)
      ├── imageset-config.yaml        # ImageSetConfiguration (generate が生成)
      ├── disconnected-overrides.yaml # disconnected 設定 (generate が生成)
      ├── pull-secret.json            # Pull secret (collect が保存)
      ├── .image-size-cache.json      # サイズキャッシュ (自動管理)
      └── backups/                    # collect 時の差分バックアップ
          └── YYYYMMDD-HHMMSS/
              └── collected-data.json # 前回の collected-data.json
```

**設計方針**:
- ベースディレクトリを固定することで `--output-dir` の指定ミスによるデータの散逸を防ぐ
- クラスタ名のサブディレクトリにより、複数クラスタのデータを共存可能（ただし1クラスタのみの場合は自動検出）
- `collect` を再実行するとその時点の情報で上書きされ、前回データと差分がある場合のみ `backups/` にバックアップを作成
- `exclusions.json` は `collect` では上書きされない（exclude/include コマンドでのみ変更）

## Usage

### 1. collect — クラスタから情報を収集

稼働中のクラスタに接続し、Subscription / CSV / CatalogSource / Pod イメージ / ワークロード定義などを収集する。
イメージサイズと OCP Platform サイズも同時に取得する。

```bash
oc-mirctl collect --kubeconfig ~/.kube/config
```

出力: `mirror_data/<cluster-name>/collected-data.json`

collect はオプションなしで、その時点でクラスタに登録されている情報を **使用中のものも未使用のものも** すべて収集する。
何を対象にするかの絞り込みは `list` / `generate` 側で行う。

| 指定 | 対象 |
|---|---|
| デフォルト | 使用中 + 待機中のワークロードのイメージ |
| `--running-only` | 使用中 (動いている Pod のイメージ) のみ |
| `--all` | 使用中 + 待機中 + 未使用 (`--running-only` とは併用不可) |

収集する情報 (使用中):
- OCP バージョン・チャネル・リリースイメージ
- OCP Platform イメージサイズ (`oc adm release info --size`)
- 全 Subscription (パッケージ名・チャネル・CatalogSource) と CSV の `spec.version`
- CSV の `spec.relatedImages` (NVIDIA 等のサードパーティイメージ)
- 全 CatalogSource とそのインデックスイメージ
- 動いている Pod のコンテナイメージ (完了済み = `Succeeded` / `Failed` の Pod は除く)
- 全イメージのサイズ (skopeo)

収集する情報 (待機中のワークロード、デフォルトで対象・`--running-only` で除外):
- Deployment / StatefulSet / DaemonSet / Job / CronJob / DeploymentConfig の Pod テンプレートが参照するが、
  動いている Pod では使われていないイメージ (例: replicas=0 の Deployment、次回実行を待つ CronJob、
  対象ノードの無い DaemonSet)。disconnected 環境では、ミラーしていないとスケールアップやノード追加の時点で
  `ImagePullBackOff` になるため、デフォルトで含める
- platform / Operator の relatedImages でカバーされるもの、内部レジストリのイメージは除く
- 完了済みの Job / CronJob の Pod が使っていたイメージは、Job / CronJob の定義からここに含まれる
- タグも digest も無い参照は、実行中の digest が分からないため `:latest` を補う

収集する情報 (未使用、`--all` で対象。ダウングレードへの備え):
- ClusterVersion の `status.history` に残る過去のリリース
- InstallPlan に残る、過去にインストールした Operator の版 (その版を含むチャネルも記録)
- ノードにキャッシュされているが、使用中のもの・リリースに含まれるものではないイメージ

未使用の情報の制約:
- kubelet はノードの `status.images` にサイズの大きい順で最大 50 件しか載せないため、小さいイメージは検出できない場合がある
  (上限に達したノードがあると collect が警告する)。ディスク逼迫時の GC で消えたイメージも検出できない
- ノードが削除・入れ替えされると (オートスケール等)、そのノードのキャッシュも失われる
- InstallPlan は OLM により古いものから削除されるため、古い版は残っていない場合がある
- OCP 本体のダウングレードは基本的にサポートされない (z-stream 内のロールバックのみ)。ミラーを用意しても戻せるとは限らない

### 2. list — 収集済みデータを一覧表示

```bash
# 全 Operator と relatedImages をサイズ付きで表示 (使用中 + 待機中のワークロード)
oc-mirctl list

# 動いている Pod のイメージだけを表示
oc-mirctl list --running-only

# 未使用の過去のリリース / Operator の版 / キャッシュ済みイメージも表示
oc-mirctl list --all

# JSON / YAML 出力
oc-mirctl list -o json
oc-mirctl list -o yaml
```

サイズ情報は `collect` 時に取得済みのデータを使用するため、オフラインでも即座に表示される。

### 3. exclude / include — mirror 対象の調整

不要な Operator やイメージを除外リストで管理する。`generate` 時に除外リストが反映される。

```bash
# Operator を除外
oc-mirctl exclude operator odf-operator --reason "顧客環境ではストレージ別途"

# イメージをフル参照で除外
oc-mirctl exclude image "vllm/vllm-openai:v0.28.0" --reason "ラボ検証用"

# CSV の relatedImages name で除外
oc-mirctl exclude image --name "odh_workbench_jupyter_pytorch_rocm_py312_image" --reason "ROCm不要"

# パターンマッチで一括除外 (relatedImages / Pod イメージ / 待機中・未使用のイメージが対象)
oc-mirctl exclude image --match "*rocm*" --reason "ROCm不要"
oc-mirctl exclude image --match "*gaudi*" --reason "Gaudi不要"

# 除外リストを確認
oc-mirctl exclude-list

# 除外を復帰
oc-mirctl include operator odf-operator
oc-mirctl include image --match "*rocm*"
```

出力: `mirror_data/<cluster-name>/exclusions.json`

### 4. generate — mirror 用設定ファイルを生成

`collected-data.json` を元に oc-mirror 用の設定を生成する。

```bash
# ImageSetConfiguration のみ生成 (使用中 + 待機中のワークロード、Operator は現行〜チャネル最新)
oc-mirctl generate

# 動いている Pod のイメージだけを対象にする (待機中のワークロードのイメージを除く)
oc-mirctl generate --running-only

# Operator を現行バージョンに固定 (次のアップグレード先は含めない)
oc-mirctl generate --pin-operators

# 未使用の過去のリリース / Operator の版 / キャッシュ済みイメージも含めて生成 (ダウングレードへの備え)
oc-mirctl generate --all

# 組み合わせも可 (過去の版〜現行の範囲に固定)
oc-mirctl generate --all --pin-operators

# Mirror Registry の URL 指定で disconnected-overrides.yaml も生成
oc-mirctl generate --mirror-registry mirror.example.com:8443
```

出力:
- `imageset-config.yaml` — oc-mirror 用 ImageSetConfiguration
- `disconnected-overrides.yaml` — install-config に追加する CA 証明書・IDMS 設定 (`--mirror-registry` 指定時)

ImageSetConfiguration の特徴:
- Subscription から正確にパッケージ名・チャネルを取得
- 各 Operator に `minVersion` を設定し、古いバージョンの不要なイメージを除外
  - バージョンは CSV の `spec.version` を使う (旧データでは CSV 名から推定)
  - デフォルト: `minVersion` のみ (現行〜チャネル最新。アップグレード先も事前にミラーできる)
  - `--pin-operators`: `minVersion = maxVersion` (現行バージョンのみ。容量を最小化)
- Subscription のチャネルがパッケージの defaultChannel と異なる場合の扱い
  - oc-mirror はフィルタ後のカタログに defaultChannel が残らないとエラーにする
  - デフォルト: defaultChannel をチャネル名のみで追加する (oc-mirror はその head 1 版をミラーする)
  - `--pin-operators`: defaultChannel は追加せず、パッケージの `defaultChannel` を
    Subscription のチャネルで上書きする (余分な版をミラーしない)
- `--all` のときの追加分
  - Platform: 過去のリリースをマイナーバージョンごとのチャネル (例: `stable-4.20`) に min/max で追加
  - Operator: 現チャネルの `minVersion` を最も古くインストールした版に下げる。別チャネルにある過去の版は、
    そのチャネルをインストールした版の範囲で追加する (カタログから消えた版は警告して除外)
  - additionalImages: ノードにキャッシュされた未使用イメージを追加
- 既にミラー済みの古いイメージはレジストリから自動削除されない。不要になったイメージの削除は
  `oc-mirror delete` (DeleteImageSetConfiguration) を別途運用で実施する
- CatalogSource ごとに Operator をグルーピング (redhat / certified / community)
  - `spec.image` を持たない CatalogSource は、デフォルトカタログ名から対応するインデックスを使う
    (例: `certified-operators` → `certified-operator-index:v<OCP minor>`)。
    それ以外のカタログで image が不明な場合はエラーにする
- relatedImages + Pod イメージからサードパーティイメージを自動収集
  - タグも digest も無い Pod イメージは、collect 時に実行中の digest (status の imageID) で解決する
- 待機中のワークロードのイメージを additionalImages に追加 (デフォルト。`--running-only` で除外)

### 5. mirror — oc-mirror を実行

```bash
# 直接実行 (デフォルト)
oc-mirctl mirror

# スクリプトを出力して後で実行
oc-mirctl mirror --dump-script > run-mirror.sh
chmod +x run-mirror.sh
```

### 6. apply — IDMS/CatalogSource をクラスタに適用

oc-mirror が生成した IDMS / ITMS / CatalogSource をクラスタに適用する。
**既存の IDMS エントリとマージし、上書きしない。**

```bash
# dry-run で確認
oc-mirctl apply \
  --results-dir ./mirror_data/oc-mirror-workspace/results-1234567890 \
  --dry-run

# 適用
oc-mirctl apply \
  --results-dir ./mirror_data/oc-mirror-workspace/results-1234567890
```

マージロジック:
- **ImageDigestMirrorSet**: `source` をキーにして既存エントリを保持し、新規 source のみ追加
- **ImageTagMirrorSet**: 同上
- **CatalogSource**: `spec.image` が変更された場合のみ更新

## End-to-End ワークフロー

```
collect ──→ list ──→ exclude/include ──→ generate ──→ mirror ──→ apply
  │           │         │                   │           │          │
  │  クラスタから  │  内容を確認   │  不要なものを除外    │  設定ファイル  │  oc-mirror  │  IDMS 等を
  │  情報収集     │              │                    │  を生成       │  を実行     │  クラスタに適用
```

```bash
# 1. 稼働中クラスタから情報収集
oc-mirctl collect

# 2. 収集結果を確認
oc-mirctl list

# 3. 不要なものを除外 (必要に応じて)
oc-mirctl exclude image --match "*rocm*" --reason "ROCm不要"
oc-mirctl exclude image --match "*gaudi*" --reason "Gaudi不要"

# 4. 除外結果を確認
oc-mirctl exclude-list
oc-mirctl list

# 5. mirror 設定を生成
oc-mirctl generate --mirror-registry mirror.example.com:8443

# 6. imageset-config.yaml を確認・編集 (必要に応じて)
vim mirror_data/<cluster-name>/imageset-config.yaml

# 7. oc-mirror でミラーリング実行
oc-mirctl mirror

# 8. IDMS/CatalogSource をクラスタに適用
oc-mirctl apply \
  --results-dir ./mirror_data/oc-mirror-workspace/results-*
```

## Mirror Registry のセットアップ

Mirror Registry (EC2 VM) のプロビジョニングと mirror-registry のインストールは Ansible 側で管理する。

```bash
cd aws-sno-ipi
./run-playbook.sh playbooks/mirror-setup.yml
```

## 既知の制約

### graph-image と oc-mirror v2 のホスト名問題

oc-mirror v2 は `graph: true` で graph-image を push する際、`go-containerregistry` ライブラリを使用する（通常のイメージミラーリングは `containers/image` を使用）。`go-containerregistry` は内部的にレジストリの **ホスト名** (`hostname -f`) を使って push するため、destination に外部 IP を指定すると認証やブロブ書き込みが失敗する。

**回避策**: `oc-mirctl mirror --dump-script` が生成するスクリプトは、Mirror VM 上で実行される際に destination を自動的に内部ホスト名 (`$(hostname -f):8443`) に解決する。pull-secret にも外部 IP と内部ホスト名の両方の認証を追加する。

**条件**: dump-script は Mirror VM 上で実行すること。リモートから `docker://<外部IP>:8443` を直接指定しての実行は graph-image の push で失敗する。

### MAXIMUM_LAYER_SIZE と nginx

mirror-registry (Quay) の `config.yaml` に `MAXIMUM_LAYER_SIZE: 1024G` を設定しても、nginx の `client_max_body_size` は自動的に更新されない（`/v2/` の一部の location のみ反映される）。Ansible のセットアップスクリプトで `MAXIMUM_LAYER_SIZE` の設定と nginx の `client_max_body_size` の両方を修正する。

## Requirements

- `oc` — OpenShift CLI (collect 時にクラスタ接続・Platform サイズ取得に使用)
- `oc-mirror` — mirror サブコマンドで使用 (Mirror VM にインストール)
- `skopeo` — collect 時のイメージサイズ取得に使用 (オプション、なければスキップ)
- Go 1.23+ — ビルド時のみ
