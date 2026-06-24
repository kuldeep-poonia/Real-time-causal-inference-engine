document.addEventListener('DOMContentLoaded', () => {
    // DOM Elements
    const healthBadge = document.getElementById('health-status-badge');
    const nodesList = document.getElementById('nodes-list');
    
    // Panel States
    const stateWelcome = document.getElementById('welcome-message');
    const stateLoading = document.getElementById('loading-spinner');
    const stateWaiting = document.getElementById('waiting-data');
    const stateResults = document.getElementById('diagnostic-results');
    
    // Result Elements
    const resultTitle = document.getElementById('result-title');
    const resultConfidence = document.getElementById('result-confidence');
    const metricLoad = document.getElementById('metric-load');
    const metricExpected = document.getElementById('metric-expected');
    
    const listCauses = document.getElementById('list-causes');
    const listActions = document.getElementById('list-actions');
    const sectionActions = document.getElementById('section-actions');

    const headerTime = document.getElementById('header-time');
    const headerNodes = document.getElementById('header-active-nodes');

    let currentNodeId = null;

    // ----- Clock -----
    function updateClock() {
        const now = new Date();
        headerTime.textContent = now.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    }
    setInterval(updateClock, 1000);
    updateClock();

    // ----- API Polling -----

    async function checkHealth() {
        try {
            const res = await fetch('/health');
            if (!res.ok) throw new Error('Health check failed');
            const data = await res.json();
            
            if (data.ready) {
                healthBadge.textContent = 'System Healthy';
                healthBadge.className = 'health-status health-ok';
            } else {
                healthBadge.textContent = 'System Degraded';
                healthBadge.className = 'health-status health-warn';
            }
        } catch (err) {
            healthBadge.textContent = 'System Offline';
            healthBadge.className = 'health-status health-error';
        }
    }

    async function fetchNodes() {
        try {
            const res = await fetch('/nodes');
            if (!res.ok) throw new Error('Failed to fetch nodes');
            const data = await res.json();
            
            const count = data.nodes ? data.nodes.length : 0;
            headerNodes.textContent = `${count} Active ${count === 1 ? 'Service' : 'Services'}`;

            renderNodes(data.nodes || []);
        } catch (err) {
            console.error(err);
        }
    }

    // ----- UI Rendering -----

    function renderNodes(nodes) {
        if (nodes.length === 0) {
            nodesList.innerHTML = '<div class="loading-text">No services discovered yet.</div>';
            return;
        }

        const currentActive = currentNodeId;
        nodesList.innerHTML = '';

        nodes.forEach(node => {
            const el = document.createElement('div');
            el.className = `node-item ${node.node_id === currentActive ? 'active' : ''}`;
            
            // Format ID nicely
            let displayName = node.container_name || node.node_id;
            if (displayName.length > 25) {
                displayName = displayName.substring(0, 22) + '...';
            }

            el.innerHTML = `
                <div class="node-name">${displayName}</div>
                <div class="node-id">Status: ${node.status || 'Active'}</div>
            `;
            
            el.addEventListener('click', () => selectNode(node));
            nodesList.appendChild(el);
        });
    }

    function setPanelState(state) {
        [stateWelcome, stateLoading, stateWaiting, stateResults].forEach(el => el.classList.add('hidden'));
        state.classList.remove('hidden');
    }

    async function selectNode(node) {
        currentNodeId = node.node_id;
        fetchNodes(); // Re-render to show active state
        
        if (!node.pipeline_ready) {
            setPanelState(stateWaiting);
            return;
        }

        setPanelState(stateLoading);

        try {
            // Trigger Diagnostic Analysis
            const res = await fetch('/analyze', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ target_node: node.node_id, time_steps: 5 })
            });

            if (!res.ok) throw new Error('Analysis failed');
            const data = await res.json();

            // Handle Unknown or fallback states
            if (data.confidence_state === "UnknownState" || !data.success) {
                setPanelState(stateWaiting);
                return;
            }

            renderResults(data, node);
        } catch (err) {
            console.error(err);
            setPanelState(stateWaiting);
        }
    }

    function renderResults(data, node) {
        setPanelState(stateResults);
        
        const sectionExplanation = document.getElementById('section-explanation');
        const explanationText = document.getElementById('explanation-text');
        const explanationNarrative = document.getElementById('explanation-narrative');

        resultTitle.textContent = data.incident_title || 'Service Analysis';
        
        // Confidence badge
        let confClass = 'conf-low';
        let confText = 'Low Confidence';
        if (data.confidence_score > 0.8) {
            confClass = 'conf-high';
            confText = 'High Confidence';
        } else if (data.confidence_score > 0.5) {
            confClass = 'conf-med';
            confText = 'Moderate Confidence';
        }
        
        resultConfidence.className = `confidence-badge ${confClass}`;
        resultConfidence.textContent = `${confText} (${(data.confidence_score * 100).toFixed(0)}%)`;

        // Basic Metrics
        metricLoad.textContent = node.load ? node.load.toFixed(2) : '-';
        metricExpected.textContent = '1.00'; // Baseline

        // Explanation (Why?)
        if (data.confidence_narrative || (data.narrative && data.narrative.length > 0)) {
            sectionExplanation.classList.remove('hidden');
            explanationText.textContent = data.confidence_narrative || "Analysis completed.";
            
            explanationNarrative.innerHTML = '';
            if (data.narrative && data.narrative.length > 0) {
                data.narrative.forEach(item => {
                    const li = document.createElement('li');
                    li.innerHTML = `<div class="insight-value">${item}</div>`;
                    explanationNarrative.appendChild(li);
                });
            }
        } else {
            sectionExplanation.classList.add('hidden');
        }

        // Causes
        listCauses.innerHTML = '';
        if (data.causes && Object.keys(data.causes).length > 0) {
            for (const [cause, weight] of Object.entries(data.causes)) {
                const li = document.createElement('li');
                li.innerHTML = `
                    <div class="insight-header">
                        <span class="insight-value">${cause}</span>
                        <span class="insight-value">${(weight * 100).toFixed(1)}%</span>
                    </div>
                    <div class="insight-sub">Impact Weight</div>
                `;
                listCauses.appendChild(li);
            }
        } else {
            listCauses.innerHTML = '<li><div class="insight-value">System functioning normally</div></li>';
        }

        // Actions / Remediation
        listActions.innerHTML = '';
        if (data.remediation && data.remediation.length > 0) {
            sectionActions.classList.remove('hidden');
            data.remediation.forEach(action => {
                const li = document.createElement('li');
                li.innerHTML = `
                    <div class="insight-header">
                        <span class="insight-value">${action.action_type || 'Intervention'}</span>
                        <span class="insight-sub">Confidence: ${(action.expected_confidence * 100).toFixed(0)}%</span>
                    </div>
                    <div class="insight-sub">${action.description || 'Apply system adjustments to restore baseline.'}</div>
                `;
                listActions.appendChild(li);
            });
        } else {
            sectionActions.classList.add('hidden');
        }
    }

    // ----- Initialization -----

    checkHealth();
    fetchNodes();

    setInterval(checkHealth, 5000);
    setInterval(fetchNodes, 5000);
});
