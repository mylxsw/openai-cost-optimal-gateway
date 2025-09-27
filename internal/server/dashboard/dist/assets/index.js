(function () {
  const e = React.createElement;
  const { useState, useEffect, useCallback, useMemo } = React;

  function App() {
    const [apiKey, setApiKey] = useState(localStorage.getItem('gateway_api_key') || '');
    const [limit, setLimit] = useState(50);
    const [records, setRecords] = useState([]);
    const [summary, setSummary] = useState(null);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');
    const [lastUpdated, setLastUpdated] = useState(null);

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
      fetch(`/usage?limit=${limit}`, {
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
          setRecords(Array.isArray(data.data) ? data.data : []);
          setSummary(data.summary || null);
          setLastUpdated(new Date());
        })
        .catch((err) => {
          setError(err.message || '获取使用数据失败');
        })
        .finally(() => setLoading(false));
    }, [apiKey, limit]);

    useEffect(() => {
      fetchUsage();
      if (!apiKey) {
        return undefined;
      }
      const timer = setInterval(fetchUsage, 15000);
      return () => clearInterval(timer);
    }, [fetchUsage, apiKey]);

    const rows = useMemo(() => {
      if (!records.length) {
        return e(
          'tr',
          { className: 'empty-row' },
          e('td', { colSpan: 7 }, '暂无数据或未配置 API Key')
        );
      }
      return records.map((item) => {
        const createdAt = item.created_at ? new Date(item.created_at) : null;
        const createdText = createdAt ? createdAt.toLocaleString() : '未知时间';
        const duration = typeof item.duration === 'string' ? item.duration : item.duration || '';
        return e(
          'tr',
          { key: item.id || `${item.created_at}-${item.provider}-${item.model}` },
          e('td', null, createdText),
          e('td', null, item.path || '-'),
          e('td', null, item.provider || '-'),
          e('td', null, item.model || '-'),
          e('td', { className: 'number-cell' }, item.request_tokens ?? '-'),
          e('td', { className: 'number-cell' }, item.response_tokens ?? '-'),
          e('td', null, duration)
        );
      });
    }, [records]);

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
        e('h1', null, 'OpenAI Cost Optimal Gateway Usage Dashboard'),
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
            'button',
            { type: 'submit', className: 'refresh-button' },
            loading ? '加载中...' : '刷新'
          )
        ),
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
              e('th', null, '时间'),
              e('th', null, '路径'),
              e('th', null, '服务商'),
              e('th', null, '模型'),
              e('th', null, '输入 Token'),
              e('th', null, '输出 Token'),
              e('th', null, '耗时')
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
