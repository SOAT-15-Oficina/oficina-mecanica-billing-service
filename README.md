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

## Fluxo

O Billing so reage a comandos do OS Service (fila `billing-inbox.fifo`) e a
dois atores externos: o cliente, pelos links do e-mail, e o Mercado Pago,
pelo webhook. Tudo o que ele tem a dizer sai como evento no topico
`billing-events.fifo`, que o OS Service assina.

```mermaid
sequenceDiagram
    autonumber
    participant OS as OS Service
    participant BI as Billing
    actor CLI as Cliente
    participant MP as Mercado Pago

    OS-)BI: billing.create-quote (itens congelados + cliente)
    BI->>CLI: e-mail com links de aprovacao
    BI--)OS: billing.quote-sent
    CLI->>BI: GET /billing/public/quotes/{id}/approve-all
    BI--)OS: billing.quote-approved (ou quote-rejected)
    OS-)BI: billing.request-payment
    BI->>MP: POST /checkout/preferences
    BI->>CLI: e-mail com o link de pagamento
    BI--)OS: billing.payment-requested
    CLI->>MP: paga
    MP->>BI: POST /billing/webhooks/mercadopago
    BI->>MP: GET /v1/payments/{id}
    BI--)OS: billing.payment-confirmed (ou payment-failed)
    OS-)BI: billing.refund-payment (compensacao, caso C)
    BI->>MP: POST /v1/payments/{id}/refunds
    BI--)OS: billing.payment-refunded
```

### Mensagens

| Recebe | Efeito | Responde |
|---|---|---|
| `billing.create-quote` | grava `quotes` e `quote_items` (um orçamento por OS; repetição devolve o mesmo) e envia o e-mail | `billing.quote-sent` |
| `billing.request-payment` | cria a preferência no Mercado Pago com `expiration_date_to = agora + PAYMENT_EXPIRATION`, grava o pagamento `PENDENTE` e envia o link por e-mail; um pagamento pendente ainda válido é reaproveitado | `billing.payment-requested` |
| `billing.refund-payment` | estorna o pagamento `APROVADO` (chave de idempotência = `message_id`) e marca `ESTORNADO` | `billing.payment-refunded` (campos vazios quando não há pagamento aprovado) |
| `billing.cancel-quote` | orçamento `CANCELADO`, pagamentos `PENDENTE` viram `EXPIRADO` | `billing.quote-canceled` (sem `quote_id` quando não há orçamento) |
| qualquer outro tipo | marcado como processado e logado (`billing.message_ignored`) | — |

| Origem | Evento publicado |
|---|---|
| cliente decide o último item pendente, com ao menos um aprovado | `billing.quote-approved` (itens aprovados, reprovados e total aprovado) |
| cliente decide o último item pendente, todos reprovados | `billing.quote-rejected` (`CLIENTE`) |
| webhook com pagamento `approved` | `billing.payment-confirmed` |
| webhook com pagamento `rejected` ou `cancelled` | `billing.payment-failed` (`RECUSADO`) |

`billing.request-payment` para um orçamento inexistente ou não aprovado
responde `billing.payment-failed` (`RECUSADO`, `payment_id` nulo) para a Saga
compensar em vez de esperar o prazo.

### Garantias

- **Uma transação por mensagem**: `pgoutbox.MarkProcessed`, o efeito e o
  `pgoutbox.Enqueue` da resposta são gravados juntos; o relay do outbox
  publica depois. Nenhum handler publica direto.
- **Chamadas externas fora da transação**: preferência e estorno no Mercado
  Pago acontecem antes da transação, depois de uma checagem em
  `processed_messages`. Se a transação falhar, a mensagem volta para a fila e
  o estorno é repetido com a mesma chave de idempotência.
- **E-mail depois do commit**: falha de envio é só logada
  (`billing.email_failed`), como no monólito; o cliente continua tendo o
  resumo em `/billing/public/quotes/{id}`.
- **Webhook não é fonte da verdade**: a assinatura é validada, o corpo é
  ignorado e o status vem de `GET /v1/payments/{id}`. Notificação repetida
  (mesmo `provider_payment_id`) não gera evento novo.
- **Processo único**: API, consumidor da `billing-inbox` e relay do outbox
  são goroutines do mesmo processo e param juntos no `SIGTERM`.

## Endpoints

Vistos pelo cliente com o prefixo `/api` (o gateway remove).

| Método e rota | Auth | Função |
|---|---|---|
| `GET /billing/quotes/by-work-order/{workOrderId}` | JWT `admin` ou `employee` | orçamento, itens, decisões e pagamentos |
| `GET /billing/public/quotes/{quoteId}` | público | resumo para o cliente, com `payment_url` enquanto houver pagamento pendente |
| `GET /billing/public/quotes/{quoteId}/items/{workOrderServiceId}/approve` | público | aprova um item |
| `GET /billing/public/quotes/{quoteId}/items/{workOrderServiceId}/reject` | público | reprova um item |
| `GET /billing/public/quotes/{quoteId}/approve-all` | público | aprova todos os pendentes |
| `GET /billing/public/quotes/{quoteId}/reject-all` | público | reprova todos os pendentes |
| `POST /billing/webhooks/mercadopago?data.id=&type=payment` | `x-signature` | notificação do Mercado Pago |
| `GET /ping`, `GET /ready` | público | liveness e readiness |
| `GET /billing/swagger`, `GET /billing/docs/swagger.yaml` | público | contrato |

Decidir um item já decidido, ou qualquer item de orçamento aprovado,
reprovado ou cancelado, responde **409**. Assinatura inválida no webhook
responde **401**; falha ao consultar o Mercado Pago responde **500** para ele
reenviar.

A assinatura segue o Mercado Pago: `x-signature: ts=<ts>,v1=<hex>`, onde `v1`
é o HMAC-SHA256, com `MERCADOPAGO_WEBHOOK_SECRET`, do manifesto
`id:<data.id>;request-id:<x-request-id>;ts:<ts>;`.

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

## Configuração

| Variável | Padrão | Uso |
|---|---|---|
| `SERVER_PORT`, `SERVER_ENVIRONMENT` | `8080` | HTTP e ambiente |
| `APP_BASE_URL` | — | URL pública com `/api`; base dos links do e-mail |
| `JWT_SECRET_KEY` | — | mesmo segredo HS256 da Lambda de autenticação |
| `DATABASE_*` | — | PostgreSQL próprio |
| `AWS_DEFAULT_REGION`, `AWS_ENDPOINT_URL` | `sa-east-1` | SQS/SNS/SES; endpoint só no LocalStack |
| `MESSAGING_INBOX_QUEUE_URL` | — | `billing-inbox.fifo`; vazio desliga o consumidor |
| `MESSAGING_EVENTS_TOPIC_ARN` | — | `billing-events.fifo` |
| `MESSAGING_POLL_WAIT` | `20s` | long polling do consumidor |
| `OUTBOX_POLL_INTERVAL` | `500ms` | intervalo do relay do outbox |
| `MERCADOPAGO_BASE_URL` | `https://api.mercadopago.com` | API real ou o fake |
| `MERCADOPAGO_ACCESS_TOKEN`, `MERCADOPAGO_WEBHOOK_SECRET` | — | segredos |
| `MERCADOPAGO_NOTIFICATION_URL` | `{APP_BASE_URL}/billing/webhooks/mercadopago` | `notification_url` da preferência |
| `PAYMENT_EXPIRATION` | `48h` | `expiration_date_to` da preferência |
| `EMAIL_PROVIDER` | `mailhog` | `mailhog` ou `ses` |
| `EMAIL_HOST`, `EMAIL_PORT`, `EMAIL_FROM` | `localhost`, `1025` | mailhog |
| `SES_SENDER_EMAIL`, `SES_REPLY_TO`, `SES_CONFIG_SET` | — | SES (credenciais pelo IRSA) |

Se o provedor de e-mail não puder ser criado, o serviço sobe sem e-mail e loga
o erro, em vez de entrar em crashloop.

## Como rodar

```bash
cp .env.example .env
docker compose up --build
curl localhost:8091/ping
```

O compose sobe API (8091), PostgreSQL (5433), LocalStack (4566, filas e
tópico criados por `local/localstack/init.sh`), mailhog (8025) e o fake do
Mercado Pago (8095). As portas do host podem ser trocadas por
`API_HOST_PORT`, `DB_HOST_PORT`, `LOCALSTACK_HOST_PORT`, `MAILHOG_HOST_PORT` e
`MPFAKE_HOST_PORT`.

### Testando o pagamento com o fake do Mercado Pago

`cmd/mpfake` é um Mercado Pago em memória (`docker build --target mpfake`).
Implementa `POST /checkout/preferences`, `GET /v1/payments/{id}`,
`POST /v1/payments/{id}/refunds` (idempotente por `X-Idempotency-Key`),
`GET /__health`, a página `GET /checkout/{preferenceId}` com os botões
Pagar e Recusar e `POST /checkout/{preferenceId}/pay?status=approved|rejected`,
que cria o pagamento e chama a `notification_url` com `x-signature` assinada
por `MERCADOPAGO_WEBHOOK_SECRET`.

1. Publique um `billing.create-quote` na fila (no compose do
   `-infrastructure` quem faz isso é o OS Service):

   ```bash
   WO=$(uuidgen); ITEM=$(uuidgen)
   docker compose exec localstack awslocal sqs send-message \
     --queue-url http://localhost:4566/000000000000/billing-inbox.fifo \
     --message-group-id "$WO" --message-deduplication-id "$(uuidgen)" \
     --message-body '{"message_id":"'"$(uuidgen)"'","type":"billing.create-quote","version":1,
       "saga_id":"'"$(uuidgen)"'","work_order_id":"'"$WO"'","correlation_id":"local",
       "occurred_at":"2026-11-11T10:00:00Z","payload":{"work_order_code":"OS-1",
       "customer":{"name":"Maria","email":"maria@example.com"},
       "items":[{"work_order_service_id":"'"$ITEM"'","title":"Troca de oleo","price_cents":15000,"estimated_time_minutes":30}]}}'
   ```

2. Abra o e-mail em <http://localhost:8025> e clique em **Aprovar todos**.
3. Publique `billing.request-payment` com o mesmo `work_order_id` e payload
   `{"quote_id":"<id do orcamento>"}`. O link de pagamento chega por e-mail e
   aparece em `GET /billing/public/quotes/{quoteId}` (`payment_url`).
4. Abra o link (página do fake em `localhost:8095`) e clique em **Pagar** ou
   **Recusar**. O fake chama o webhook, o Billing consulta o pagamento e
   publica `billing.payment-confirmed` ou `billing.payment-failed`.

Para ver os eventos, assine uma fila FIFO no tópico `billing-events.fifo` com
`RawMessageDelivery=true`.

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

Os testes de `internal/billing`, `internal/database` e `pgoutbox` usam
PostgreSQL de verdade e são pulados sem `DATABASE_HOST` (no CI, um service
container). Mercado Pago, e-mail e SQS/SNS são substituídos por fakes; o
cliente do Mercado Pago é testado contra `httptest.Server` e o fake, de ponta
a ponta, com o próprio cliente.

| Pacote | Cobertura |
|---|---|
| `internal/auth` | 91,7% |
| `internal/billing` | 90,2% |
| `internal/config` | 100% |
| `internal/database` | 89,2% |
| `internal/mercadopago` | 98,7% |
| `internal/messaging` | 95,6% |
| `internal/messaging/pgoutbox` | 88,6% |
| `internal/mpfake` | 100% |
| `internal/observability` | 85,7% |
| `internal/routes` | 100% |
| `internal/routes/middlewares` | 96,0% |
| `packages/email` | 98,8% |
| **total (métrica do gate)** | **94,6%** |

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
- Mensagens: `internal/messaging/catalog.go` (idêntico nos três serviços) e
  catálogo na RFC-0006, seção 5.
- JWT: `internal/auth/testdata/token.golden` é o mesmo fixture dos outros
  repositórios.
