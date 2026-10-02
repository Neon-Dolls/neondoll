import { workflow, node, trigger, splitInBatches, nextBatch, expr } from '@n8n/workflow-sdk';

const webhook = trigger({
  type: 'n8n-nodes-base.webhook',
  version: 2.1,
  config: {
    name: 'Webhook',
    parameters: {
      httpMethod: 'POST',
      path: 'c0b2aecb-8897-4363-b7d2-775e12a9074b',
      responseMode: 'responseNode',
      options: {}
    }
  },
  output: [{ body: {} }]
});

const expandPages = node({
  type: 'n8n-nodes-base.code',
  version: 2,
  config: {
    name: 'Expand Pages',
    parameters: {
      mode: 'runOnceForAllItems',
      jsCode: 'const input = $input.first().json;\nconst body = input.body || input;\nconst batch_id = body.batch_id;\nconst univelop_record_id = body.univelop_record_id;\nconst univelop_list_id = body.univelop_list_id;\nconst payload = body.payload || {};\nconst book_title = payload.book_title;\nconst pages = payload.pages;\nif (!pages || !Array.isArray(pages)) {\n  return [{ json: { error: "No pages array in payload", received: JSON.stringify(input).substring(0, 500) } }];\n}\nvar result = [];\nfor (var i = 0; i < pages.length; i++) {\n  var page = pages[i];\n  result.push({\n    json: {\n      batch_id: batch_id,\n      univelop_record_id: univelop_record_id,\n      univelop_list_id: univelop_list_id,\n      book_title: book_title,\n      prompt: page.prompt,\n      seed: page.seed,\n      page_index: i + 1,\n      total_pages: pages.length\n    }\n  });\n}\nreturn result;'
    }
  },
  output: [{ batch_id: 'FCCB-2609', prompt: 'test', seed: 42, page_index: 1, total_pages: 50 }]
});

const sibNode = splitInBatches({
  version: 3,
  config: {
    name: 'Submit to Marble',
    parameters: {
      batchSize: 1,
      options: {}
    }
  }
});

const submitJob = node({
  type: 'n8n-nodes-base.httpRequest',
  version: 4.3,
  config: {
    name: 'Submit Job',
    parameters: {
      method: 'POST',
      url: 'http://marble:8080/v1/render',
      sendBody: true,
      specifyBody: 'json',
      jsonBody: expr('{ "prompt": "{{ $json.prompt }}", "seed": {{ $json.seed }} }'),
      options: {}
    }
  },
  output: [{ statusCode: 200 }]
});

const aggregateResults = node({
  type: 'n8n-nodes-base.code',
  version: 2,
  config: {
    name: 'Aggregate Results',
    parameters: {
      mode: 'runOnceForAllItems',
      jsCode: 'var items = $input.all();\nvar batch_id = items[0] && items[0].json && items[0].json.batch_id;\nvar book_title = items[0] && items[0].json && items[0].json.book_title;\nvar total = items.length;\nvar successCount = 0;\nvar failedCount = 0;\nfor (var i = 0; i < items.length; i++) {\n  var code = items[i].json && items[i].json.statusCode;\n  if (code && code !== 200) {\n    failedCount++;\n  } else {\n    successCount++;\n  }\n}\nreturn [{\n  json: {\n    batch_id: batch_id || null,\n    book_title: book_title || null,\n    total_pages: total,\n    submitted: successCount,\n    failed: failedCount,\n    status: failedCount > 0 ? "partial" : "success",\n    timestamp: new Date().toISOString()\n  }\n}];'
    }
  },
  output: [{ batch_id: 'FCCB-2609', total_pages: 50, submitted: 50, failed: 0, status: 'success' }]
});

const respond = node({
  type: 'n8n-nodes-base.respondToWebhook',
  version: 1.1,
  config: {
    name: 'Respond',
    parameters: {
      respondWith: expr('={{firstEntryJson}}'),
      redirectURL: '',
      options: {
        responseHeaders: {
          entries: [
            { name: 'Content-Type', value: 'application/json' }
          ]
        }
      }
    }
  },
  output: [{ body: { status: 'success' } }]
});

export default workflow('WvPoKwj6QA80zz06', 'Create Marble Batch')
  .add(webhook)
  .to(expandPages)
  .to(sibNode
    .onDone(aggregateResults.to(respond))
    .onEachBatch(submitJob.to(nextBatch(sibNode)))
  );