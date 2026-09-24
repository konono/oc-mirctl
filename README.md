# oc-mirctl

稼働中の OpenShift クラスタから disconnected インストールに必要な情報を収集し、mirror 用の設定ファイル生成・ミラーリング実行・クラスタへの適用までを行う CLI ツール。

## Install

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
      ├── collected-data.json         # 収集データ (collect が生成・更新)
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
- `collect` を再実行すると前回データとマージされ、差分がある場合のみ `backups/` にバックアップを作成
- `exclusions.json` は `collect` では上書きされない（exclude/include コマンドでのみ変更）

## Usage

### 1. collect — クラスタから情報を収集

稼働中のクラスタに接続し、Subscription / CSV / CatalogSource / Pod イメージを収集する。
イメージサイズと OCP Platform サイズも同時に取得する。

```bash
oc-mirctl collect --kubeconfig ~/.kube/config
```

出力: `mirror_data/<cluster-name>/collected-data.json`

収集する情報:
- OCP バージョン・チャネル・リリースイメージ
- OCP Platform イメージサイズ (`oc adm release info --size`)
- 全 Subscription (パッケージ名・チャネル・CatalogSource)
- CSV の `spec.relatedImages` (NVIDIA 等のサードパーティイメージ)
- 全 CatalogSource とそのインデックスイメージ
- 全 Pod で使用中のコンテナイメージ
- 全イメージのサイズ (skopeo)

### 2. list — 収集済みデータを一覧表示

```bash
# 全 Operator と relatedImages をサイズ付きで表示
oc-mirctl list

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

# パターンマッチで一括除外
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
# ImageSetConfiguration のみ生成
oc-mirctl generate

# Mirror Registry の URL 指定で disconnected-overrides.yaml も生成
oc-mirctl generate --mirror-registry mirror.example.com:8443
```

出力:
- `imageset-config.yaml` — oc-mirror 用 ImageSetConfiguration
- `disconnected-overrides.yaml` — install-config に追加する CA 証明書・IDMS 設定 (`--mirror-registry` 指定時)

ImageSetConfiguration の特徴:
- Subscription から正確にパッケージ名・チャネルを取得
- 各 Operator に `minVersion` を設定し、古いバージョンの不要なイメージを除外
- CatalogSource ごとに Operator をグルーピング (redhat / certified / community)
- relatedImages + Pod イメージからサードパーティイメージを自動収集

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
