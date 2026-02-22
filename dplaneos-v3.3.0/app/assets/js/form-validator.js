// D-PlaneOS - Form Validation

class FormValidator {
  constructor(form) {
    this.form = form;
    this.rules = new Map();
    this.errors = new Map();
    this.init();
  }
  
  init() {
    this.form.addEventListener('submit', (e) => this.handleSubmit(e));
    
    // Real-time validation on blur
    this.form.querySelectorAll('input, textarea, select').forEach(field => {
      field.addEventListener('blur', () => this.validateField(field));
      field.addEventListener('input', () => this.clearFieldError(field));
    });
  }
  
  addRule(fieldName, rules) {
    this.rules.set(fieldName, rules);
  }
  
  addRules(rulesObject) {
    Object.entries(rulesObject).forEach(([field, rules]) => {
      this.addRule(field, rules);
    });
  }
  
  validateField(field) {
    const name = field.name || field.id;
    const rules = this.rules.get(name);
    
    if (!rules) return true;
    
    const value = field.value.trim();
    
    // Required
    if (rules.required && !value) {
      this.setFieldError(field, rules.required.message || 'This field is required');
      return false;
    }
    
    // Min length
    if (rules.minLength && value.length < rules.minLength.value) {
      this.setFieldError(field, rules.minLength.message || `Minimum ${rules.minLength.value} characters`);
      return false;
    }
    
    // Max length
    if (rules.maxLength && value.length > rules.maxLength.value) {
      this.setFieldError(field, rules.maxLength.message || `Maximum ${rules.maxLength.value} characters`);
      return false;
    }
    
    // Pattern
    if (rules.pattern && !rules.pattern.value.test(value)) {
      this.setFieldError(field, rules.pattern.message || 'Invalid format');
      return false;
    }
    
    // Email
    if (rules.email) {
      const emailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
      if (!emailRegex.test(value)) {
        this.setFieldError(field, rules.email.message || 'Invalid email address');
        return false;
      }
    }
    
    // Custom validation
    if (rules.custom) {
      const result = rules.custom(value);
      if (result !== true) {
        this.setFieldError(field, result);
        return false;
      }
    }
    
    // Match field
    if (rules.match) {
      const matchField = this.form.querySelector(`[name="${rules.match.field}"]`);
      if (matchField && value !== matchField.value) {
        this.setFieldError(field, rules.match.message || 'Fields do not match');
        return false;
      }
    }
    
    this.clearFieldError(field);
    return true;
  }
  
  setFieldError(field, message) {
    field.classList.add('error');
    field.classList.remove('success');
    
    let errorEl = field.parentElement.querySelector('.form-error');
    if (!errorEl) {
      errorEl = document.createElement('div');
      errorEl.className = 'form-error';
      field.parentElement.appendChild(errorEl);
    }
    
    errorEl.innerHTML = `
      <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
        <path d="M8 15A7 7 0 1 1 8 1a7 7 0 0 1 0 14zm0 1A8 8 0 1 0 8 0a8 8 0 0 0 0 16z"/>
        <path d="M7.002 11a1 1 0 1 1 2 0 1 1 0 0 1-2 0zM7.1 4.995a.905.905 0 1 1 1.8 0l-.35 3.507a.552.552 0 0 1-1.1 0L7.1 4.995z"/>
      </svg>
      ${message}
    `;
    errorEl.style.display = 'flex';
    
    this.errors.set(field.name || field.id, message);
  }
  
  clearFieldError(field) {
    field.classList.remove('error');
    
    const errorEl = field.parentElement.querySelector('.form-error');
    if (errorEl) {
      errorEl.style.display = 'none';
    }
    
    if (field.value.trim()) {
      field.classList.add('success');
    } else {
      field.classList.remove('success');
    }
    
    this.errors.delete(field.name || field.id);
  }
  
  validate() {
    this.errors.clear();
    
    const fields = Array.from(this.form.querySelectorAll('input, textarea, select'))
      .filter(field => this.rules.has(field.name || field.id));
    
    let isValid = true;
    fields.forEach(field => {
      if (!this.validateField(field)) {
        isValid = false;
      }
    });
    
    return isValid;
  }
  
  handleSubmit(e) {
    if (!this.validate()) {
      e.preventDefault();
      
      // Focus first error
      const firstError = this.form.querySelector('.error');
      if (firstError) {
        firstError.focus();
        firstError.scrollIntoView({ behavior: 'smooth', block: 'center' });
      }
      
      if (window.EnhancedUI) {
        EnhancedUI.toast('Please fix the errors in the form', 'error');
      }
    }
  }
  
  reset() {
    this.errors.clear();
    this.form.querySelectorAll('.error, .success').forEach(field => {
      field.classList.remove('error', 'success');
    });
    this.form.querySelectorAll('.form-error').forEach(el => {
      el.style.display = 'none';
    });
  }
}

// Helper for quick validation
window.validateForm = (formId, rules) => {
  const form = document.getElementById(formId) || document.querySelector(formId);
  if (!form) return null;
  
  const validator = new FormValidator(form);
  validator.addRules(rules);
  return validator;
};

// Common validation rules
window.ValidationRules = {
  required: (message = 'This field is required') => ({
    required: { message }
  }),
  
  email: (message = 'Invalid email address') => ({
    email: { message }
  }),
  
  minLength: (length, message) => ({
    minLength: {
      value: length,
      message: message || `Minimum ${length} characters required`
    }
  }),
  
  maxLength: (length, message) => ({
    maxLength: {
      value: length,
      message: message || `Maximum ${length} characters allowed`
    }
  }),
  
  pattern: (regex, message = 'Invalid format') => ({
    pattern: {
      value: regex,
      message
    }
  }),
  
  match: (fieldName, message) => ({
    match: {
      field: fieldName,
      message: message || 'Fields do not match'
    }
  }),
  
  password: () => ({
    minLength: { value: 8, message: 'Password must be at least 8 characters' },
    pattern: {
      value: /^(?=.*[a-z])(?=.*[A-Z])(?=.*\d)/,
      message: 'Password must contain uppercase, lowercase, and number'
    }
  })
};
