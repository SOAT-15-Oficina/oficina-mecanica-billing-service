# oficina-mecanica-billing-service

Orçamento, decisão do cliente e cobrança das ordens de serviço da oficina
mecânica. Participante da Saga orquestrada pelo
[OS Service](https://github.com/SOAT-15-Oficina/oficina-mecanica-os-service).

A arquitetura do sistema completo, as RFCs e as ADRs vivem no
[oficina-mecanica-infrastructure](https://github.com/SOAT-15-Oficina/oficina-mecanica-infrastructure/tree/main/docs),
em especial a [RFC-0006](https://github.com/SOAT-15-Oficina/oficina-mecanica-infrastructure/blob/main/docs/rfc/0006-microsservicos-e-saga.md).

## Responsabilidade

| Faz | Não faz |
|---|---|
| gera e envia o orçamento a partir dos itens congelados recebidos da OS | decide o que entra na OS |
| registra a decisão do cliente, item a item, pelos links públicos | altera o status da OS (só publica eventos) |
| cobra pelo Mercado Pago Checkout Pro e confirma por consulta à API | lê o banco de outro serviço |
| estorna quando a Saga compensa | |

## Arquitetura do serviço

```mermaid
flowchart LR
    OS[OS Service<br/>orquestrador] -->|comandos| IN[[billing-inbox.fifo]]
    IN --> API
    subgraph pod [billing-service]
        API[API HTTP + consumidor]
        PUB[publicador do outbox]
    end
    API --> DB[(PostgreSQL<br/>RDS próprio)]
    PUB --> DB
    PUB --> TOP{{billing-events.fifo}}
    TOP --> OSIN[[os-inbox.fifo]]
    CLI[Cliente] -->|/billing/public/quotes| API
    MP[Mercado Pago] -->|webhook| API
    API -->|preferências, consulta, estorno| MP
```

| Peça | Tecnologia |
|---|---|
| Linguagem / HTTP | Go 1.26, Fiber v3 |
| Banco | PostgreSQL 17 (RDS), migrations com goose |
| Mensageria | SQS FIFO (entrada), SNS FIFO (saída), outbox transacional |
| Pagamento | Mercado Pago Checkout Pro (ambiente de testes) |
| Observabilidade | slog JSON + Datadog APM |
| Deploy | EKS via kustomize, Terraform próprio em `infra/` |

### Modelo de dados

| Tabela | Conteúdo |
|---|---|
| `quotes` | um orçamento por OS, com cópia de nome e e-mail do cliente |
| `quote_items` | itens congelados e decisão do cliente por item |
| `payments` | preferência e pagamento no Mercado Pago; no máximo um aprovado por orçamento |
| `outbox` | eventos a publicar, gravados na mesma transação do efeito |
| `processed_messages` | `message_id` já processados, para idempotência |

## Como rodar

```bash
cp .env.example .env
docker compose up --build
curl localhost:8091/ping
```

O compose sobe API, PostgreSQL e LocalStack (SQS/SNS FIFO, criados por
`local/localstack/init.sh`).

## Testes e cobertura

```bash
go test ./... -coverprofile=coverage.out
./scripts/coverage-gate.sh coverage.out
```

O CI reprova abaixo de **80%** duas vezes: no job `test`, pelo
`scripts/coverage-gate.sh`, e no job `quality`, pelo Quality Gate
`oficina-fase4` do SonarQube (cobertura, bugs, vulnerabilidades e duplicação).
`cmd/`, `database/` e `docs/` ficam fora da métrica: são wiring e arquivos
embutidos.

| Pacote | Cobertura |
|---|---|
| total (métrica do gate) | 93,8% |

## Pipeline

| Evento | Jobs |
|---|---|
| PR para `main` ou `hml` | `lint` (actionlint, redocly, terraform validate, kubeconform), `test` (cobertura ≥ 80%), `quality` (SonarQube) |
| push em `hml` | deploy em homologação |
| push em `main` | deploy em produção |

O deploy aplica `infra/`, publica a imagem no ECR, sincroniza o segredo do
Secrets Manager para o cluster, aplica `k8s/overlays/<ambiente>`, roda as
migrations num Job e confere `/ping` e o contrato servido.

## Contrato

- HTTP: [`docs/swagger.yaml`](docs/swagger.yaml), servido em `/api/billing/swagger`.
- Mensagens: catálogo na RFC-0006, seção 5.
