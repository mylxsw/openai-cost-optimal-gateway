(function () {
  const e = React.createElement;
  const { useState, useEffect, useCallback, useMemo } = React;

  const NS_PER_SECOND = 1e9;
  const NS_PER_MILLISECOND = 1e6;
  const DURATION_UNIT_FACTORS = {
    ns: 1,
    us: 1e3,
    µs: 1e3,
    μs: 1e3,
    ms: 1e6,
    s: NS_PER_SECOND,
    m: 60 * NS_PER_SECOND,
    h: 3600 * NS_PER_SECOND,
  };

  function parseGoDuration(value) {
    if (typeof value !== 'string' || value.trim() === '') {
      return null;
    }
    const regex = /(-?\d+(?:\.\d+)?)(ns|us|µs|μs|ms|s|m|h)/g;
    let total = 0;
    let matched = false;
    let match;
    while ((match = regex.exec(value)) !== null) {
      const amount = parseFloat(match[1]);
      const unit = match[2];
      const factor = DURATION_UNIT_FACTORS[unit];
      if (!Number.isFinite(amount) || !factor) {
        continue;
      }
      matched = true;
      total += amount * factor;
    }
    if (!matched) {
      return null;
    }
    return total;
  }

  function formatDuration(value) {
    if (value === null || value === undefined || value === '') {
      return '-';
    }

    let nsValue;
    if (typeof value === 'number') {
      nsValue = value;
    } else if (typeof value === 'string') {
      const numeric = Number(value);
      if (!Number.isNaN(numeric)) {
        nsValue = numeric;
      } else {
        const parsed = parseGoDuration(value);
        if (parsed === null) {
          return value;
        }
        nsValue = parsed;
      }
    } else {
      return '-';
    }

    if (!Number.isFinite(nsValue)) {
      return '-';
    }

    const absNs = Math.abs(nsValue);
    if (absNs >= NS_PER_SECOND) {
      const seconds = nsValue / NS_PER_SECOND;
      const decimals = Math.abs(seconds) >= 10 ? 0 : 2;
      return `${seconds.toFixed(decimals)} s`;
    }

    if (absNs >= NS_PER_MILLISECOND) {
      const milliseconds = nsValue / NS_PER_MILLISECOND;
      const decimals = Math.abs(milliseconds) >= 10 ? 0 : 2;
      return `${milliseconds.toFixed(decimals)} ms`;
    }

    return `${nsValue} ns`;
  }

  function App() {
    const [apiKey, setApiKey] = useState(localStorage.getItem('gateway_api_key') || '');
    const [limit, setLimit] = useState(50);
    const [records, setRecords] = useState([]);
    const [summary, setSummary] = useState(null);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');
    const [lastUpdated, setLastUpdated] = useState(null);
    const [requestIdFilter, setRequestIdFilter] = useState('');

    useEffect(() => {
      if (apiKey) {
        localStorage.setItem('gateway_api_key', apiKey);
      } else {
        localStorage.removeItem('gateway_api_key');
      }
    }, [apiKey]);

    const fetchUsage = useCallback(() => {
      if (!apiKey) {
        setRecords([]);
        setSummary(null);
        setLastUpdated(null);
        return;
      }
      setLoading(true);
      setError('');
      const query = new URLSearchParams({ limit: String(limit) });
      if (requestIdFilter) {
        query.set('request_id', requestIdFilter);
      }
      fetch(`/usage?${query.toString()}`, {
        headers: {
          Authorization: `Bearer ${apiKey}`,
        },
      })
        .then((res) => {
          if (!res.ok) {
            throw new Error(`请求失败：${res.status}`);
          }
          return res.json();
        })
        .then((data) => {
          const rawRecords = Array.isArray(data.data) ? data.data : [];
          // 过滤掉无效的初始数据记录
          const validRecords = rawRecords.filter(record => {
            if (!record || !record.created_at) return false;
            const createdAt = new Date(record.created_at);
            // 过滤掉明显无效的日期 (如 1/1/1) 和空数据
            return createdAt.getFullYear() > 1900 && 
                   (record.request_id || record.model || record.provider);
          });
          setRecords(validRecords);
          setSummary(data.summary || null);
          setLastUpdated(new Date());
        })
        .catch((err) => {
          setError(err.message || '获取使用数据失败');
        })
        .finally(() => setLoading(false));
    }, [apiKey, limit, requestIdFilter]);

    useEffect(() => {
      fetchUsage();
      if (!apiKey) {
        return undefined;
      }
      const timer = setInterval(fetchUsage, 15000);
      return () => clearInterval(timer);
    }, [fetchUsage, apiKey]);

    const rows = useMemo(() => {
      const columnCount = 7;
      if (!records.length) {
        return e(
          'tr',
          { className: 'empty-row' },
          e(
            'td',
            { colSpan: columnCount },
            requestIdFilter ? '没有匹配的请求记录' : '暂无数据或未配置 API Key'
          )
        );
      }

      const stack = (children, opts = {}) =>
        e(
          'div',
          {
            style: {
              display: 'flex',
              flexDirection: 'column',
              alignItems: opts.align || 'flex-start',
              gap: opts.gap || '2px',
            },
          },
          ...children.filter(Boolean)
        );

      const text = (value, style = {}) =>
        e('span', {
          style: {
            fontSize: '0.85em',
            color: '#d5d8dc',
            wordBreak: 'break-word',
            ...style,
          },
        }, value);

      const strong = (value, style = {}) =>
        e('span', {
          style: {
            fontSize: '0.95em',
            color: '#ecf0f1',
            fontWeight: 500,
            wordBreak: 'break-word',
            ...style,
          },
        }, value);

      return records.map((item) => {
        const createdAt = item.created_at ? new Date(item.created_at) : null;
        const createdText = createdAt ? createdAt.toLocaleString() : '未知时间';
        const requestId = item.request_id || '';
        const promptTokens = item.request_tokens ?? '-';
        const completionTokens = item.response_tokens ?? '-';
        const firstLatency = formatDuration(item.first_token_latency);
        const totalLatency = formatDuration(item.duration);
        const attempt = item.attempt && item.attempt > 0 ? item.attempt : null;
        const statusCode = item.status_code;

        let computedStatus = item.status || '';
        if (!computedStatus && typeof statusCode === 'number' && statusCode > 0) {
          computedStatus = statusCode >= 200 && statusCode < 400 ? 'success' : 'failure';
        }
        const isSuccess = computedStatus === 'success';
        const isFailure = computedStatus === 'failure';
        const statusColor = isSuccess ? '#58d68d' : isFailure ? '#f1948a' : '#bdc3c7';
        const statusLabel = statusCode ? `${statusCode}${attempt ? ` (#${attempt})` : ''}` : attempt ? `#${attempt}` : '-';

        const highlight = Boolean(requestIdFilter && requestId && requestIdFilter === requestId);
        const rowKey = item.id || `${requestId || 'req'}-${item.created_at || ''}-${item.provider || ''}-${item.model || ''}-${attempt || 0}`;

        const requestNode = requestId
          ? e(
              'button',
              {
                type: 'button',
                onClick: () => setRequestIdFilter(requestId),
                style: {
                  background: 'none',
                  border: 'none',
                  color: '#5dade2',
                  cursor: 'pointer',
                  padding: 0,
                  fontSize: '0.85em',
                  textAlign: 'left',
                  wordBreak: 'break-all',
                },
                title: '点击筛选该请求',
              },
              requestId
            )
          : text('-');

        const statusContent = e(
          'div',
          { style: { display: 'flex', alignItems: 'center', gap: '6px' } },
          strong(statusLabel, { color: statusColor }),
          isFailure && item.error
            ? e(
                'span',
                {
                  title: item.error,
                  onClick: (evt) => {
                    evt.stopPropagation();
                    window.alert(item.error);
                  },
                  style: {
                    color: '#f5b7b1',
                    cursor: 'help',
                    fontSize: '0.9em',
                    display: 'inline-flex',
                    alignItems: 'center',
                  },
                },
                '⚠'
              )
            : null
        );

        return e(
          'tr',
          {
            key: rowKey,
            style: highlight ? { backgroundColor: 'rgba(90, 160, 255, 0.08)' } : undefined,
          },
          e(
            'td',
            null,
            stack([
              strong(createdText),
              requestNode,
            ])
          ),
          e(
            'td',
            null,
            stack([
              strong(item.original_model || '-'),
              text(item.model ? `→ ${item.model}` : '→ -', { color: '#95a5a6' }),
            ])
          ),
          e(
            'td',
            null,
            stack([
              strong(item.provider || '-'),
            ])
          ),
          e(
            'td',
            null,
            strong(`${promptTokens} / ${completionTokens}`),
          ),
          e(
            'td',
            null,
            stack([
              statusContent,
              !isSuccess && !isFailure && computedStatus ? text(computedStatus, { color: '#bdc3c7' }) : null,
            ])
          ),
          e(
            'td',
            null,
            stack([
              strong(firstLatency),
              text(totalLatency, { color: '#95a5a6' }),
            ])
          )
        );
      });
    }, [records, requestIdFilter]);

    const summaryCards = useMemo(() => {
      const metrics = summary || { total_requests: 0, total_prompt_tokens: 0, total_completion_tokens: 0 };
      const items = [
        { label: '请求次数', value: metrics.total_requests },
        { label: '输入 Token', value: metrics.total_prompt_tokens },
        { label: '输出 Token', value: metrics.total_completion_tokens },
      ];
      return items.map((item) =>
        e(
          'div',
          { key: item.label, className: 'summary-card' },
          e('div', { className: 'summary-label' }, item.label),
          e('div', { className: 'summary-value' }, item.value)
        )
      );
    }, [summary]);

    return e(
      'div',
      { className: 'app-container' },
      e(
        'header',
        { className: 'app-header' },
        e('h1', null, 'Dashboard'),
        e('p', null, '查看最近的请求与 Token 使用情况。')
      ),
      e(
        'section',
        { className: 'control-panel' },
        e(
          'form',
          {
            className: 'control-form',
            onSubmit: (evt) => {
              evt.preventDefault();
              fetchUsage();
            },
          },
          e(
            'label',
            null,
            'API Key',
            e('input', {
              type: 'password',
              placeholder: '请输入网关 API Key',
              value: apiKey,
              onChange: (evt) => setApiKey(evt.target.value.trim()),
            })
          ),
          e(
            'label',
            null,
            '记录条数',
            e(
              'select',
              {
                value: limit,
                onChange: (evt) => setLimit(Number(evt.target.value)),
              },
              [20, 50, 100, 200].map((val) =>
                e('option', { value: val, key: val }, val)
              )
            )
          ),
          e(
            'label',
            null,
            '请求 ID 筛选',
            e('input', {
              type: 'text',
              placeholder: '点击表格中的请求 ID 自动填充',
              value: requestIdFilter,
              onChange: (evt) => setRequestIdFilter(evt.target.value.trim()),
              spellCheck: false,
              autoComplete: 'off',
            })
          ),
          e(
            'button',
            { type: 'submit', className: 'refresh-button' },
            loading ? '加载中...' : '刷新'
          )
        ),
        requestIdFilter
          ? e(
              'div',
              {
                style: {
                  marginTop: '8px',
                  display: 'flex',
                  alignItems: 'center',
                  gap: '12px',
                  flexWrap: 'wrap',
                },
                className: 'active-request-filter',
              },
              e('span', null, `当前请求 ID：${requestIdFilter}`),
              e(
                'button',
                {
                  type: 'button',
                  onClick: () => setRequestIdFilter(''),
                  style: {
                    background: 'none',
                    border: '1px solid rgba(255, 255, 255, 0.2)',
                    color: '#ec7063',
                    padding: '4px 8px',
                    borderRadius: '4px',
                    cursor: 'pointer',
                  },
                },
                '清除请求筛选'
              )
            )
          : null,
        lastUpdated
          ? e('div', { className: 'last-updated' }, `最近更新：${lastUpdated.toLocaleString()}`)
          : null,
        error ? e('div', { className: 'error-banner' }, error) : null
      ),
      e('section', { className: 'summary-section' }, summaryCards),
      e(
        'section',
        { className: 'table-section' },
        e(
          'table',
          null,
          e(
            'thead',
            null,
            e(
              'tr',
              null,
              e('th', null, '时间 / 请求 ID'),
              e('th', null, '模型 (原始/实际)'),
              e('th', null, '服务商'),
              e('th', null, 'Token (I/O)'),
              e('th', null, '状态 (序号)'),
              e('th', null, '耗时 (首字符/总)')
            )
          ),
          e('tbody', null, rows)
        )
      )
    );
  }

  const container = document.getElementById('root');
  if (container) {
    const root = ReactDOM.createRoot(container);
    root.render(e(App));
  }
})();
